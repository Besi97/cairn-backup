package agent

import "time"

type clock interface {
	Now() time.Time
	NewTimer(time.Duration) timer
}

type timer interface {
	C() <-chan time.Time
	Stop()
}

type wallClock struct{}

func (wallClock) Now() time.Time                 { return time.Now() }
func (wallClock) NewTimer(d time.Duration) timer { return wallTimer{time.NewTimer(d)} }

type wallTimer struct{ *time.Timer }

func (t wallTimer) C() <-chan time.Time { return t.Timer.C }
func (t wallTimer) Stop()               { t.Timer.Stop() }
