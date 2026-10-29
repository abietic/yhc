"""Bounded, recoverable logical clocks for cold-service coordination."""

import threading
import time


_MAX_NS = (1 << 63) - 1
_POLICIES = {"logical_pause", "logical_elapsed"}


class ClockError(ValueError):
    """Clock input or state is invalid."""


def _fail(message):
    raise ClockError(message)


def _ns(value):
    if isinstance(value, bool) or not isinstance(value, int):
        _fail("clock nanoseconds must be integers")
    if value < 0 or value > _MAX_NS:
        _fail("clock nanoseconds are out of range")
    return value


def _rate(value):
    if isinstance(value, bool) or not isinstance(value, int):
        _fail("clock rate must be an integer")
    if value <= 0 or value > 1000000:
        _fail("clock rate is out of range")
    return value


def _version(value):
    if isinstance(value, bool) or not isinstance(value, int) or value != 1:
        _fail("clock snapshot version is unsupported")
    return value


def _add(left, right):
    if left > _MAX_NS - right:
        _fail("clock calculation overflow")
    return left + right


class RecoverableClock:
    """A thread-safe logical clock with explicit cold-recovery boundaries."""

    def __init__(self, policy, rate_num=1, rate_den=1, *,
                 wall_ns=time.time_ns, monotonic_ns=time.monotonic_ns):
        if not isinstance(policy, str) or policy not in _POLICIES:
            _fail("clock policy is unsupported")
        if not callable(wall_ns) or not callable(monotonic_ns):
            _fail("clock source is invalid")
        self._policy = policy
        self._rate_num = _rate(rate_num)
        self._rate_den = _rate(rate_den)
        self._wall_ns = wall_ns
        self._monotonic_ns = monotonic_ns
        initial_mono = _ns(monotonic_ns())
        self._lock = threading.RLock()
        self._logical_ns = 0
        self._anchor_wall_ns = None
        self._mono_start_ns = initial_mono
        self._last_mono_ns = initial_mono
        self._state = "running"

    @property
    def policy(self):
        return self._policy

    @property
    def rate_num(self):
        return self._rate_num

    @property
    def rate_den(self):
        return self._rate_den

    def _observe_mono(self):
        observed = _ns(self._monotonic_ns())
        if observed < self._last_mono_ns:
            _fail("clock monotonic source moved backwards")
        self._last_mono_ns = observed
        return observed

    def _scaled(self, elapsed):
        if elapsed and elapsed > _MAX_NS // self._rate_num:
            _fail("clock calculation overflow")
        return (elapsed * self._rate_num) // self._rate_den

    def _value_locked(self):
        if self._state == "frozen":
            return self._logical_ns
        now_mono = self._observe_mono()
        if self._state == "scheduled" and now_mono < self._mono_start_ns:
            return self._logical_ns
        return _add(self._logical_ns, self._scaled(now_mono - self._mono_start_ns))

    def value_ns(self):
        with self._lock:
            return self._value_locked()

    def status(self):
        with self._lock:
            if self._state == "frozen":
                return "frozen"
            now_mono = self._observe_mono()
            if self._state == "scheduled" and now_mono < self._mono_start_ns:
                return "scheduled"
            return "running"

    def freeze(self):
        with self._lock:
            if self._state == "frozen":
                return self._snapshot_locked()
            logical = self._value_locked()
            wall = _ns(self._wall_ns())
            self._logical_ns = logical
            self._anchor_wall_ns = wall
            self._state = "frozen"
            return self._snapshot_locked()

    def _snapshot_locked(self):
        if self._state != "frozen":
            _fail("clock snapshot requires frozen state")
        return {
            "version": 1,
            "policy": self._policy,
            "logical_ns": self._logical_ns,
            "wall_ns": self._anchor_wall_ns,
            "rate_num": self._rate_num,
            "rate_den": self._rate_den,
        }

    def snapshot(self):
        with self._lock:
            return self._snapshot_locked()

    def restore(self, snapshot, *, reference_wall_ns, max_downtime_ns):
        with self._lock:
            if self._state != "frozen":
                _fail("clock restore requires frozen state")
            if not isinstance(snapshot, dict) or set(snapshot) != {
                    "version", "policy", "logical_ns", "wall_ns", "rate_num", "rate_den"}:
                _fail("clock snapshot schema is invalid")
            _version(snapshot["version"])
            if not isinstance(snapshot["policy"], str) or snapshot["policy"] not in _POLICIES:
                _fail("clock policy is unsupported")
            rate_num = _rate(snapshot["rate_num"])
            rate_den = _rate(snapshot["rate_den"])
            if snapshot["policy"] != self._policy or rate_num != self._rate_num or rate_den != self._rate_den:
                _fail("clock snapshot configuration mismatches target")
            logical = _ns(snapshot["logical_ns"])
            source_wall = _ns(snapshot["wall_ns"])
            reference = _ns(reference_wall_ns)
            bound = _ns(max_downtime_ns)
            if bound == 0:
                _fail("clock downtime bound must be positive")
            if reference < source_wall:
                _fail("clock reference wall moved backwards")
            downtime = reference - source_wall
            if downtime > bound:
                _fail("clock downtime exceeds bound")
            if self._policy == "logical_elapsed":
                logical = _add(logical, self._scaled(downtime))
            self._logical_ns = logical
            self._anchor_wall_ns = reference
            return self._snapshot_locked()

    def schedule_resume(self, at_wall_ns):
        with self._lock:
            if self._state != "frozen":
                _fail("clock resume requires frozen state")
            target = _ns(at_wall_ns)
            now_wall = _ns(self._wall_ns())
            now_mono = self._observe_mono()
            if target <= now_wall or target < self._anchor_wall_ns:
                _fail("clock resume wall is not in the future")
            delay = target - now_wall
            mono_start = _add(now_mono, delay)
            logical = self._logical_ns
            if self._policy == "logical_elapsed":
                logical = _add(logical, self._scaled(target - self._anchor_wall_ns))
            self._logical_ns = logical
            self._anchor_wall_ns = target
            self._mono_start_ns = mono_start
            self._state = "scheduled"
