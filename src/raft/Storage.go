package raft

import (
	"fmt"
	"log"
	"math"
	"strings"
	"util"
)

type Storage struct{
	// Discovered from log entries
	FirstIndex int64
	LastTerm int32
	LastIndex int64
	// All committed entries are immediately applied to Raft it self,
	// but may asynchronously be applied to Service
	CommitIndex int64
	state *State

	node *Node
	// notify Raft there is new entry to be replicated
	C chan int

	// entries may not be continuous(for follower)
	entries map[int64]*Entry
	Service Service
	
	db Db
}

func NewStorage(node *Node, db Db) *Storage {
	st := new(Storage)
	st.state = NewState()
	st.entries = make(map[int64]*Entry)
	
	st.db = db
	st.node = node
	st.C = make(chan int, 10)

	st.FirstIndex = math.MaxInt64

	st.loadState()
	st.loadEntries()

	return st
}

func (st *Storage)Close(){
	st.SaveState()
	if st.db != nil {
		st.db.Close()
	}
}

/* #################### State ###################### */

func (st *Storage)State() *State{
	return st.state
}

func (st *Storage)loadState() {
	data := st.db.Get("@State")
	st.state.Decode(data)
	if st.state.Members == nil {
		st.state.Members = make(map[string]string)
	}
}

func (st *Storage)SaveState(){
	st.state.Term = st.node.Term
	st.state.VoteFor = st.node.VoteFor
	st.state.Members = make(map[string]string)
	
	st.state.Members[st.node.Id] = st.node.Addr
	for _, m := range st.node.Members {
		st.state.Members[m.Id] = m.Addr
	}
	
	log.Printf("save raft state[%s]:", st.node.Id)
	log.Println("    ", st.state.Encode())

	st.db.Set("@State", st.state.Encode())
	st.Fsync()
}

/* #################### Entry ###################### */

func (st *Storage)loadEntries(){
	for k, v := range st.db.All() {
		if !strings.HasPrefix(k, "log#") {
			continue
		}
		ent := DecodeEntry(v)
		if ent == nil {
			log.Fatal("bad entry format:", v)
		}

		st.entries[ent.Index] = ent
		st.CommitIndex = util.MaxInt64(st.LastIndex, ent.Index)
		st.FirstIndex  = util.MinInt64(st.FirstIndex, ent.Index)
		st.LastTerm    = util.MaxInt32(st.LastTerm, ent.Term)
		st.LastIndex   = util.MaxInt64(st.LastIndex, ent.Index)
	}
}

func (st *Storage)GetEntry(index int64) *Entry{
	return st.entries[index]
}

func (st *Storage)AppendEntry(type_ EntryType, data string) *Entry{
	ent := new(Entry)
	ent.Type = type_
	ent.Term = st.node.Term
	ent.Index = st.LastIndex + 1
	ent.Commit = st.CommitIndex
	ent.Data = data

	st.WriteEntry(*ent)
	// notify xport to send
	st.C <- 0
	return ent
}

// 如果存在空洞, 仅仅先缓存 entry, 不更新 lastTerm 和 lastIndex
// 参数值拷贝
func (st *Storage)WriteEntry(ent Entry){
	if ent.Index <= st.CommitIndex {
		log.Println("ent.Index", ent.Index, "<", "commitIndex", st.CommitIndex)
		return
	}

	st.entries[ent.Index] = &ent
	st.FirstIndex = util.MinInt64(st.FirstIndex, ent.Index)

	// 找出连续的 entries, 更新 LastTerm 和 LastIndex,
	for{
		ent := st.GetEntry(st.LastIndex + 1)
		if ent == nil {
			break;
		}
		st.LastTerm = ent.Term
		st.LastIndex = ent.Index

		st.db.Set(fmt.Sprintf("log#%03d", ent.Index), ent.Encode())
		log.Println("[RAFT] write Log", ent.Encode())
	}
}

func (st *Storage)Fsync() {
	err := st.db.Fsync()
	if err != nil {
		log.Fatal(err)
	}
}

// TODO:
func (st *Storage)AsyncCommitEntry(commitIndex int64){
}

func (st *Storage)CommitEntry(commitIndex int64){
	// 如果存在空洞, 不会跳过空洞 commit
	commitIndex = util.MinInt64(commitIndex, st.LastIndex)
	if commitIndex <= st.CommitIndex {
		// log.Printf("msg.CommitIndex: %d <= CommitIndex: %d\n", commitIndex, st.CommitIndex)
		return
	}
	st.CommitIndex = commitIndex
	st.Fsync()
	st.ApplyEntries()
}

func (st *Storage)ApplyEntries(){
	for idx := st.node.LastApplied() + 1; idx <= st.CommitIndex; idx ++ {
		ent := st.GetEntry(idx)
		if ent == nil {
			log.Fatalf("entry#%d not found", idx)
		}
		st.node.ApplyEntry(ent)
		// TODO: 需要存储 Raft 自己的 lastApplied
	}

	// TODO: async
	if st.Service != nil {
		for idx := st.Service.LastApplied() + 1; idx <= st.CommitIndex; idx ++ {
			ent := st.GetEntry(idx)
			if ent == nil {
				log.Printf("lost entry#%d, svc.LastApplied: %d, notify Service to install snapshot",
						idx, st.Service.LastApplied())
				st.Service.InstallSnapshot()
				break
			}
			st.Service.ApplyEntry(ent)
		}
	}
}

/* #################### Snapshot ###################### */

func (st *Storage)CreateSnapshot() *Snapshot {
	return NewSnapshotFromStorage(st)
}

// install 之前, Node 需要配置好 Members, 因为 SaveState() 会从 node.Members 获取
func (st *Storage)InstallSnapshot(sn *Snapshot) bool {
	st.db.CleanAll()

	st.node.Term    = sn.State().Term
	st.node.VoteFor = ""
	st.LastTerm     = sn.LastTerm()
	st.LastIndex    = sn.LastIndex()
	st.CommitIndex  = sn.LastIndex()

	for _, ent := range sn.Entries() {
		st.entries[ent.Index] = ent
		st.db.Set(fmt.Sprintf("log#%03d", ent.Index), ent.Encode())
	}
	st.SaveState()

	return true
}

func (st *Storage)CleanAll() bool {
	st.CommitIndex = 0
	st.LastTerm = 0
	st.LastIndex = 0
	st.db.CleanAll()
	st.SaveState()
	return true
}
