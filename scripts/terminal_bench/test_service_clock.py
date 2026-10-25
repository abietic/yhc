import unittest

from scripts.terminal_bench.service_clock import ClockError, RecoverableClock


class FakeTime:
    def __init__(self, wall=100, mono=10):
        self.wall = wall
        self.mono = mono

    def wall_ns(self):
        return self.wall

    def monotonic_ns(self):
        return self.mono


def clock(fake, policy="logical_pause", num=1, den=1):
    return RecoverableClock(policy, num, den, wall_ns=fake.wall_ns,
                            monotonic_ns=fake.monotonic_ns)


class RecoverableClockTests(unittest.TestCase):
    def test_pause_restore_preserves_value_and_snapshot_is_detached(self):
        source_time = FakeTime()
        source = clock(source_time)
        source_time.mono = 17
        frozen = source.freeze()
        self.assertEqual(frozen["logical_ns"], 7)
        external = source.snapshot()
        external["logical_ns"] = 99
        self.assertEqual(source.snapshot()["logical_ns"], 7)
        target_time = FakeTime(wall=130, mono=1)
        target = clock(target_time)
        target.freeze()
        target.restore(frozen, reference_wall_ns=130, max_downtime_ns=31)
        self.assertEqual(target.value_ns(), 7)

    def test_elapsed_restore_scales_downtime(self):
        source_time = FakeTime()
        source = clock(source_time, "logical_elapsed", 3, 2)
        source_time.mono = 14
        frozen = source.freeze()
        target_time = FakeTime(wall=110, mono=4)
        target = clock(target_time, "logical_elapsed", 3, 2)
        target.freeze()
        target.restore(frozen, reference_wall_ns=110, max_downtime_ns=10)
        self.assertEqual(target.value_ns(), 21)

    def test_scheduled_shared_wall_activates_from_same_logical_base(self):
        first_time, second_time = FakeTime(wall=100, mono=10), FakeTime(wall=100, mono=50)
        first, second = clock(first_time), clock(second_time)
        first.freeze()
        second.freeze()
        first.schedule_resume(120)
        second.schedule_resume(120)
        self.assertEqual(first.status(), "scheduled")
        first_time.mono, second_time.mono = 30, 70
        self.assertEqual(first.value_ns(), 0)
        self.assertEqual(second.value_ns(), 0)
        first_time.mono, second_time.mono = 33, 73
        self.assertEqual(first.status(), "running")
        self.assertEqual(first.value_ns(), 3)
        self.assertEqual(second.value_ns(), 3)
        elapsed_time = FakeTime(wall=100, mono=10)
        elapsed = clock(elapsed_time, "logical_elapsed")
        elapsed.freeze()
        elapsed.schedule_resume(120)
        self.assertEqual(elapsed.value_ns(), 20)

    def test_running_uses_monotonic_not_wall(self):
        fake = FakeTime()
        subject = clock(fake, "logical_elapsed")
        fake.mono, fake.wall = 15, 1
        self.assertEqual(subject.value_ns(), 5)
        fake.mono, fake.wall = 20, 0
        self.assertEqual(subject.value_ns(), 10)

    def test_rejects_invalid_and_malformed_data(self):
        fake = FakeTime()
        for policy in ("other", True):
            with self.assertRaises(ClockError):
                clock(fake, policy)
        for rate in (0, True, 1000001):
            with self.assertRaises(ClockError):
                clock(fake, num=rate)
        subject = clock(fake, "logical_elapsed")
        with self.assertRaises(ClockError):
            subject.snapshot()
        subject.freeze()
        good = subject.snapshot()
        for field, value in (("version", 2), ("version", True), ("logical_ns", True), ("wall_ns", -1),
                             ("rate_num", True), ("extra", 1)):
            candidate = dict(good)
            candidate[field] = value
            with self.assertRaises(ClockError):
                subject.restore(candidate, reference_wall_ns=100, max_downtime_ns=1)
        with self.assertRaises(ClockError):
            subject.restore(good, reference_wall_ns=99, max_downtime_ns=1)
        with self.assertRaises(ClockError):
            subject.restore(good, reference_wall_ns=102, max_downtime_ns=1)
        with self.assertRaises(ClockError):
            subject.restore(good, reference_wall_ns=100, max_downtime_ns=False)
        other = clock(fake, "logical_pause")
        other.freeze()
        with self.assertRaises(ClockError):
            other.restore(good, reference_wall_ns=100, max_downtime_ns=1)

    def test_monotonic_rollback_overflow_and_transactionality(self):
        fake = FakeTime()
        subject = clock(fake, "logical_elapsed")
        fake.mono = 20
        subject.freeze()
        before = subject.snapshot()
        bad = dict(before, logical_ns=(1 << 63) - 1)
        with self.assertRaises(ClockError):
            subject.restore(bad, reference_wall_ns=101, max_downtime_ns=2)
        self.assertEqual(subject.snapshot(), before)
        subject.schedule_resume(110)
        fake.mono = 19
        with self.assertRaises(ClockError):
            subject.value_ns()
        subject = clock(FakeTime(wall=(1 << 63) - 2, mono=(1 << 63) - 1))
        subject.freeze()
        with self.assertRaises(ClockError):
            subject.schedule_resume((1 << 63) - 1)


if __name__ == "__main__":
    unittest.main()
