package raft

import (
	"fmt"
	"strings"

	"myutil"
)

// Commit: commit entry#n and prior entries
type Entry struct{
	Term int32
	Index int64
	CommitIndex int64
	Type string // AddMember, DelMember, Heartbeat, Noop, Commit, Write
	Data string
}

func DecodeEntry(buf string) *Entry{
	m := new(Entry);
	if m.Decode(buf) {
		return m
	} else {
		return nil
	}
}

func (e *Entry)Encode() string{
	return fmt.Sprintf("%d %d %d %s %s", e.Term, e.Index, e.CommitIndex, e.Type, e.Data)
}

func (e *Entry)Decode(buf string) bool{
	buf = strings.Trim(buf, "\r\n")
	ps := strings.SplitN(buf, " ", 5)
	if len(ps) != 5 {
		return false
	}

	e.Term = myutil.Atoi32(ps[0])
	e.Index = myutil.Atoi64(ps[1])
	e.CommitIndex = myutil.Atoi64(ps[2])
	e.Type = ps[3]
	e.Data = ps[4]
	return true
}

func NewHeartbeatEntry(commitIndex int64) *Entry{
	ent := new(Entry)
	ent.Type = "Heartbeat"
	ent.Term = 0
	ent.Index = 0
	ent.CommitIndex = commitIndex
	return ent
}

func NewCommitEntry(commitIndex int64) *Entry{
	ent := new(Entry)
	ent.Type = "Commit"
	ent.Term = 0
	ent.Index = 0
	ent.CommitIndex = commitIndex
	return ent
}
