package cron

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseCronExpressionAndHumanDescriptions(t *testing.T) {
	expr, err := ParseCronExpression("*/15 9-17 * * 1,2")
	if err != nil {
		t.Fatalf("ParseCronExpression failed: %v", err)
		return
	}
	if len(expr.Minute.Values) != 4 || expr.Minute.Values[1] != 15 {
		t.Fatalf("unexpected minute values: %#v", expr.Minute.Values)
	}
	if len(expr.Hour.Values) != 9 || expr.Hour.Values[0] != 9 || expr.Hour.Values[8] != 17 {
		t.Fatalf("unexpected hour values: %#v", expr.Hour.Values)
	}
	if len(expr.DayWeek.Values) != 2 || expr.DayWeek.Values[0] != 1 || expr.DayWeek.Values[1] != 2 {
		t.Fatalf("unexpected weekday values: %#v", expr.DayWeek.Values)
	}

	for _, bad := range []string{"* * * *", "60 * * * *", "*/0 * * * *", "* 24 * * *"} {
		if _, err := ParseCronExpression(bad); err == nil {
			t.Fatalf("expected invalid cron %q to fail", bad)
			return
		}
	}

	if got := CronToHuman("* * * * *"); got != "every minute" {
		t.Fatalf("unexpected every-minute description: %q", got)
	}
	if got := CronToHuman("0 * * * *"); got != "every hour" {
		t.Fatalf("unexpected hourly description: %q", got)
	}
	if got := CronToHuman("30 9 * * *"); got != "daily at 09:30" {
		t.Fatalf("unexpected daily description: %q", got)
	}
}

func TestComputeNextCronRunAndMissedTasks(t *testing.T) {
	expr, err := ParseCronExpression("30 9 * * *")
	if err != nil {
		t.Fatal(err)
		return
	}
	loc := time.Local
	start := time.Date(2026, 6, 13, 9, 29, 0, 0, loc)
	next := ComputeNextCronRun(expr, start)
	if !next.Equal(time.Date(2026, 6, 13, 9, 30, 0, 0, loc)) {
		t.Fatalf("next run = %s", next)
	}

	created := start.UnixMilli()
	tasks := []Task{{ID: "missed", Cron: "30 9 * * *", CreatedAt: created}, {ID: "future", Cron: "0 10 * * *", CreatedAt: created}}
	missed := FindMissedTasks(tasks, time.Date(2026, 6, 13, 9, 31, 0, 0, loc).UnixMilli())
	if len(missed) != 1 || missed[0].ID != "missed" {
		t.Fatalf("unexpected missed tasks: %#v", missed)
	}
}

func TestTaskPersistenceCreateRemoveAndMarkFired(t *testing.T) {
	dir := t.TempDir()
	if got := GetCronFilePath(dir); got != filepath.Join(dir, ".yhc", "scheduled_tasks.json") {
		t.Fatalf("unexpected cron file path: %q", got)
	}
	if HasTasks(dir) {
		t.Fatal("empty project should not have tasks")
	}

	oneShot, err := CreateTask(dir, "* * * * *", "one shot", false)
	if err != nil {
		t.Fatalf("CreateTask one-shot failed: %v", err)
		return
	}
	recurring, err := CreateTask(dir, "*/5 * * * *", "repeat", true)
	if err != nil {
		t.Fatalf("CreateTask recurring failed: %v", err)
		return
	}
	if !HasTasks(dir) {
		t.Fatal("expected tasks after create")
	}
	tasks, err := ReadTasks(dir)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("expected two tasks, got %#v err=%v", tasks, err)
		return
	}

	if err := MarkFired(dir, oneShot.ID); err != nil {
		t.Fatalf("MarkFired one-shot failed: %v", err)
		return
	}
	tasks, _ = ReadTasks(dir)
	if len(tasks) != 1 || tasks[0].ID != recurring.ID {
		t.Fatalf("expected one-shot removed, got %#v", tasks)
	}

	if err := MarkFired(dir, recurring.ID); err != nil {
		t.Fatalf("MarkFired recurring failed: %v", err)
		return
	}
	tasks, _ = ReadTasks(dir)
	if len(tasks) != 1 || tasks[0].LastFiredAt == nil {
		t.Fatalf("expected recurring last fired timestamp, got %#v", tasks)
		return
	}

	if err := RemoveTasks(dir, []string{recurring.ID}); err != nil {
		t.Fatalf("RemoveTasks failed: %v", err)
		return
	}
	tasks, _ = ReadTasks(dir)
	if len(tasks) != 0 {
		t.Fatalf("expected no tasks after remove, got %#v", tasks)
	}
}

func TestCronDefaultsWriteOnlyYHCRoot(t *testing.T) {
	project := t.TempDir()
	if _, err := CreateTask(project, "* * * * *", "canonical only", true); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(project, ".yhc", "scheduled_tasks.json")
	if info, err := os.Stat(canonical); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("canonical cron mode=%v err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Dir(canonical)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("canonical root mode=%v err=%v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(project, ".eino-agent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new cron write touched legacy root: %v", err)
	}
}

func TestCronWritesAreAtomicPrivateAndPreserveSchedulingSemantics(t *testing.T) {
	project := t.TempDir()
	lastFired := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC).UnixMilli()
	want := []Task{{
		ID: "stable", Cron: "*/5 * * * *", Prompt: "run", CreatedAt: lastFired - 60_000,
		LastFiredAt: &lastFired, Recurring: true,
	}}
	if err := WriteTasks(project, want); err != nil {
		t.Fatal(err)
	}
	path := GetCronFilePath(project)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadTasks(project)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip=%#v err=%v", got, err)
	}
	if NextFireAt(got[0], JitterConfig{}) != NextFireAt(want[0], JitterConfig{}) {
		t.Fatal("atomic persistence changed scheduling semantics")
	}

	originalHook := writeTasksBeforePromote
	writeTasksBeforePromote = func() error { return errors.New("injected before promote") }
	t.Cleanup(func() { writeTasksBeforePromote = originalHook })
	if err := WriteTasks(project, []Task{{ID: "replacement", Cron: "* * * * *"}}); err == nil {
		t.Fatal("injected pre-promotion failure was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("failed atomic write changed target: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("failed atomic write left temporary files: %#v", entries)
	}
}

func TestReadTasksSkipsMalformedAndInvalidCron(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(GetCronFilePath(dir)), 0o700); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(GetCronFilePath(dir), []byte(`{"tasks":[{"id":"ok","cron":"* * * * *","prompt":"ok"},{"id":"bad","cron":"bad","prompt":"bad"}]}`), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	tasks, err := ReadTasks(dir)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "ok" {
		t.Fatalf("expected only valid task, got %#v err=%v", tasks, err)
		return
	}

	if err := os.WriteFile(GetCronFilePath(dir), []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	tasks, err = ReadTasks(dir)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("malformed file should return empty tasks, got %#v err=%v", tasks, err)
		return
	}
}

func TestSchedulerSessionTasksAndLockLifecycle(t *testing.T) {
	dir := t.TempDir()
	var fired []string
	scheduler := NewScheduler(dir, func(task Task) {
		fired = append(fired, task.ID)
	})
	scheduler.config = JitterConfig{}
	sessionTask := Task{ID: "session", Cron: "* * * * *", Prompt: "run", CreatedAt: time.Now().Add(-2 * time.Minute).UnixMilli()}
	scheduler.AddSessionTask(sessionTask)
	scheduler.check()
	if len(fired) != 1 || fired[0] != "session" {
		t.Fatalf("expected session task to fire, got %#v", fired)
	}
	scheduler.RemoveSessionTasks([]string{"session"})
	fired = nil
	scheduler.check()
	if len(fired) != 0 {
		t.Fatalf("removed session task should not fire, got %#v", fired)
	}

	acquired, err := TryAcquireSchedulerLock(dir)
	if err != nil || !acquired {
		t.Fatalf("expected lock acquired, acquired=%v err=%v", acquired, err)
		return
	}
	acquiredAgain, err := TryAcquireSchedulerLock(dir)
	if err != nil {
		t.Fatalf("second lock acquisition errored: %v", err)
		return
	}
	if !acquiredAgain {
		t.Fatal("same live process should reacquire lock idempotently")
	}
	ReleaseSchedulerLock(dir)
	if _, err := os.Stat(GetSchedulerLockPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("expected lock file removed, err=%v", err)
	}
}
