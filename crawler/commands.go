package crawler

import "time"

type Command interface {
	Apply(crawler *Crawler)
}

type CommandFunc func(*Crawler)

func (cf CommandFunc) Apply(c *Crawler) { cf(c) }

func SetWait(t time.Duration) Command {
	return CommandFunc(func(c *Crawler) {
		if c.Running() {
			go func() { c.wait <- t }()
		} else {
			c.Wait = t
		}
	})
}
