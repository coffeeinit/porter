// Package cron provides a real scheduler for Porter's cron jobs. Active crons
// (5-field schedule) are checked on a ticker; a due job boots a short-lived
// microVM running its job image.
package cron

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"porter/internal/types"
)

// Store is the persistence surface the runner needs.
type Store interface {
	ListAllCrons() []*types.Cron
	GetCron(id string) (*types.Cron, bool)
	TouchCron(id string) bool
	AppendDaemonLog(line string)
	PutVM(vm *types.VM)
}

// Booter boots a job VM; satisfied by the API's VMRunner.
type Booter interface {
	Boot(ctx context.Context, vm *types.VM) error
}

// Runner schedules and fires active crons.
type Runner struct {
	store  Store
	booter Booter
	poll   time.Duration
	once   sync.Once
	stop   chan struct{}
	wg     sync.WaitGroup
}

// NewRunner returns a scheduler that checks every poll interval.
func NewRunner(store Store, booter Booter, poll time.Duration) *Runner {
	if poll <= 0 {
		poll = 30 * time.Second
	}
	return &Runner{store: store, booter: booter, poll: poll, stop: make(chan struct{})}
}

// Start begins the scheduler loop.
func (r *Runner) Start() {
	r.once.Do(func() {
		r.wg.Add(1)
		go r.loop()
		log.Printf("cron: scheduler running (poll %s)", r.poll)
	})
}

// Stop halts the scheduler.
func (r *Runner) Stop() {
	close(r.stop)
	r.wg.Wait()
}

func (r *Runner) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	r.fireDue()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.fireDue()
		}
	}
}

// fireDue triggers every active cron whose schedule matches the current time.
func (r *Runner) fireDue() {
	now := time.Now()
	for _, c := range r.store.ListAllCrons() {
		if c == nil || !c.Active || c.Schedule == "" {
			continue
		}
		if !Matches(c.Schedule, now) {
			continue
		}
		// Avoid double-fire within the same minute as the last run.
		if c.LastRun != nil && c.LastRun.Format("2006-01-02 15:04") == now.Format("2006-01-02 15:04") {
			continue
		}
		r.fire(c)
	}
}

// fire boots a job VM for a cron.
func (r *Runner) fire(c *types.Cron) {
	r.store.TouchCron(c.ID)
	r.store.AppendDaemonLog(fmt.Sprintf("cron %s triggered job %s at %s", c.Name, c.JobImage, time.Now().Format(time.RFC3339)))
	if r.booter == nil {
		return
	}
	vm := &types.VM{
		ID:           fmt.Sprintf("cron-%s-%d", c.ID, time.Now().Unix()),
		Name:         c.Name + "-job",
		ProjectID:    c.ProjectID,
		ServiceName:  "cron",
		State:        types.StatePending,
		HealthStatus: types.HealthChecking,
		Image:        c.JobImage,
		ReplicaIndex: -1,
		CreatedAt:    time.Now(),
	}
	r.store.PutVM(vm)
	go func(v *types.VM) {
		if err := r.booter.Boot(context.Background(), v); err != nil {
			log.Printf("cron: boot job %s: %v", v.ID, err)
		}
	}(vm)
}

// Matches reports whether the 5-field cron expression matches the given time.
// Fields: minute hour day-of-month month day-of-week (0|7 = Sunday). Supports
// *, ranges (1-5), steps (*/2, 1-10/2), and comma lists.
func Matches(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	minute := fmt.Sprint(t.Minute())
	hour := fmt.Sprint(t.Hour())
	dom := fmt.Sprint(t.Day())
	month := fmt.Sprint(int(t.Month()))
	dow := fmt.Sprint(int(t.Weekday()))
	if int(t.Weekday()) == 0 {
		dow = "7"
	}
	return fieldMatch(fields[0], minute, 0, 59) &&
		fieldMatch(fields[1], hour, 0, 23) &&
		fieldMatch(fields[2], dom, 1, 31) &&
		fieldMatch(fields[3], month, 1, 12) &&
		fieldMatch(fields[4], dow, 0, 7)
}

// fieldMatch checks a single cron field against a value.
func fieldMatch(field, value string, min, max int) bool {
	if field == "*" {
		return true
	}
	for _, part := range strings.Split(field, ",") {
		if matchPart(part, value, min, max) {
			return true
		}
	}
	return false
}

// matchPart handles one comma-separated part: * , a-b , */n , a-b/n , or a plain value.
func matchPart(part, value string, min, max int) bool {
	step := 1
	// Split off a step suffix (e.g. "*/2", "1-5/2").
	base := part
	if i := strings.IndexByte(part, '/'); i >= 0 {
		base = part[:i]
		s, err := strconv.Atoi(part[i+1:])
		if err == nil && s > 0 {
			step = s
		}
	}

	var lo, hi int
	switch {
	case base == "*":
		lo, hi = min, max
	case strings.Contains(base, "-"):
		// Range "a-b". Values can be names for months/days; numeric only here.
		parts := strings.SplitN(base, "-", 2)
		a, err1 := strconv.Atoi(parts[0])
		b, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return false
		}
		lo, hi = a, b
	default:
		v, err := strconv.Atoi(base)
		if err != nil {
			return false
		}
		lo, hi = v, v
	}

	v, err := strconv.Atoi(value)
	if err != nil {
		return false
	}
	if v < lo || v > hi {
		return false
	}
	// Step applies within the range.
	return (v-lo)%step == 0
}
