package raft

import (
	"fmt"
	"log"
	"sort"
	"math/rand"
	"time"
	"strings"
	"sync"
	"encoding/json"

	"util"
)

type RoleType string

const(
	RoleLeader      = "leader"
	RoleFollower    = "follower"
	RoleCandidate   = "candidate"
)

const(
	ElectionTimeout    = 5 * 1000
	HeartbeatTimeout   = 4 * 1000 // TODO: ElectionTimeout/3
	ReplicationTimeout = 1 * 1000
	ReceiveTimeout     = HeartbeatTimeout * 3
)

type Node struct{
	Id string
	Addr string
	Role RoleType

	// Raft persisted state
	Term int32
	VoteFor string
	Members map[string]*Member

	// not valotile, persisted in Raft's database as CommitIndex
	lastApplied int64
	
	votesReceived map[string]string

	electionTimer int

	store *Storage
	// messages to be processed by raft
	recv_c chan *Message
	// messages to be sent to other node
	send_c chan *Message
	
	mux sync.Mutex
}

func NewNode(nodeId string, addr string, db Db) *Node{
	node := new(Node)
	node.Id = nodeId
	node.Addr = addr
	node.Role = RoleFollower
	node.Members = make(map[string]*Member)
	node.electionTimer = 2 * 1000

	node.store = NewStorage(node, db)

	node.recv_c = make(chan *Message, 3)
	node.send_c = make(chan *Message, 3)

	// init Raft state from persistent storage
	st := node.store
	node.lastApplied = st.CommitIndex
	node.Term = st.State().Term
	node.VoteFor = st.State().VoteFor
	for nodeId, nodeAddr := range st.State().Members {
		node.addMember(nodeId, nodeAddr)
	}

	log.Printf("init raft node[%s]:", node.Id)
	log.Println("    CommitIndex:", st.CommitIndex, "LastTerm:", st.LastTerm, "LastIndex:", st.LastIndex)
	log.Println("    " + st.State().Encode())

	return node
}

func (node *Node)RecvC() chan<- *Message {
	return node.recv_c
}

func (node *Node)SendC() <-chan *Message {
	return node.send_c
}

func (node *Node)SetService(svc Service){
	node.store.Service = svc
}

func (node *Node)Start(){
	go func() {
		log.Println("apply logs on startup")
		node.mux.Lock()
		node.store.ApplyEntries()
		node.mux.Unlock()
	}()
	node.StartTicker()
	node.StartCommunication()
}

func (node *Node)StartTicker(){
	go func() {
		const TimerInterval = 100
		ticker := time.NewTicker(TimerInterval * time.Millisecond)
		defer ticker.Stop()

		log.Println("setup ticker, interval:", TimerInterval)
		for {
			<- ticker.C
			node.mux.Lock()
			node.Tick(TimerInterval)
			node.mux.Unlock()
		}
	}()
}

func (node *Node)StartCommunication(){
	go func() {
		log.Println("setup communication")
		for{
			select{
			case <-node.store.C:
				// for len(node.store.C) > 0 {
				// 	<-node.store.C
				// }
				node.mux.Lock()
				node.replicateAllMembers()
				node.mux.Unlock()
			case msg := <-node.recv_c:
				node.mux.Lock()
				node.handleRaftMessage(msg)
				node.mux.Unlock()
			}
		}
	}()
}

// For testing
func (node *Node)Step(){
	node.mux.Lock()
	defer node.mux.Unlock()

	fmt.Printf("\n======= Testing: Step %s =======\n\n", node.Id)
	for {
		n := 0
		// receive
		for len(node.recv_c) > 0 {
			msg := <-node.recv_c
			log.Println("    receive < ", msg.Encode())
			node.handleRaftMessage(msg)
			n ++
		}
		// send
		if len(node.store.C) > 0 {
			<-node.store.C
			node.replicateAllMembers()
			n ++
		}
		if n == 0 {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
}

func (node *Node)Close(){
	node.store.Close()
}

func (node *Node)Tick(timeElapse int){
	if node.Role == RoleFollower || node.Role == RoleCandidate {
		if len(node.Members) > 0 {
			node.electionTimer += timeElapse
			if node.electionTimer >= ElectionTimeout {
				log.Println("start PreVote")
				node.startPreVote()
			}
		}
	} else if node.Role == RoleLeader {
		for _, m := range node.Members {
			m.ReceiveTimeout += timeElapse
			m.ReplicateTimer += timeElapse
			m.HeartbeatTimer += timeElapse

			if m.ReceiveTimeout < ReceiveTimeout {
				if m.ReplicateTimer >= ReplicationTimeout {
					if m.MatchIndex != 0 && m.NextIndex != m.MatchIndex + 1 {
						log.Printf("resend member: %s, next: %d, match: %d", m.Id, m.NextIndex, m.MatchIndex)
						m.NextIndex = m.MatchIndex + 1
					}
					node.replicateMember(m)
				}
			}
			if m.HeartbeatTimer >= HeartbeatTimeout {
				// log.Println("Heartbeat timeout for node", m.Id)
				node.pingMember(m)
			}
		}
	}
}

func (node *Node)startPreVote(){
	node.electionTimer = 0
	node.Role = RoleFollower
	node.votesReceived = make(map[string]string)
	node.broadcast(NewPreVoteMsg())
	
	// 单节点运行
	if len(node.Members) == 0 {
		node.startElection()
	}
}

func (node *Node)startElection(){
	node.electionTimer = rand.Intn(200)
	node.votesReceived = make(map[string]string)

	node.Role = RoleCandidate
	node.Term += 1
	node.VoteFor = node.Id
	node.store.SaveState()

	node.resetAllMember()
	node.broadcast(NewRequestVoteMsg())
	
	// 单节点运行
	if len(node.Members) == 0 {
		node.checkVoteResult()
	}
}

func (node *Node)checkVoteResult(){
	grant := 1
	reject := 0
	for _, res := range node.votesReceived {
		if res == "grant" {
			grant ++
		} else {
			reject ++
		}
	}
	if grant > (len(node.Members) + 1)/2 {
		node.becomeLeader()
	} else if reject > len(node.Members)/2 {
		log.Printf("grant: %d, reject: %d, total: %d", grant, reject, len(node.Members)+1)
		node.becomeFollower()
	}
}

func (node *Node)becomeFollower(){
	if node.Role == RoleFollower {
		return
	}
	node.Role = RoleFollower
	node.electionTimer = 0	
	node.resetAllMember()
}

func (node *Node)becomeLeader(){
	log.Printf("Node %s became leader", node.Id)

	node.Role = RoleLeader
	node.electionTimer = 0
	node.resetAllMember()
	for _, m := range node.Members {
		m.NextIndex = node.store.LastIndex
	}
	// write noop entry with currentTerm to implictly commit previous term's log
	if node.store.LastIndex == 0 || node.store.LastIndex != node.store.CommitIndex {
		node.store.AppendEntry(EntryTypeNoop, "")
	} else {
		node.pingAllMember()
	}
}

/* ############################################# */

func (node *Node)resetAllMember(){
	for _, m := range node.Members {
		node.resetMember(m)
	}
}

func (node *Node)resetMember(m *Member){
	m.Reset()
	m.Role = RoleFollower
}

func (node *Node)pingAllMember(){
	for _, m := range node.Members {
		node.pingMember(m)
	}
}

func (node *Node)pingMember(m *Member){
	m.HeartbeatTimer = 0
	
	ent := NewPingEntry(node.store.CommitIndex)
	prev := node.store.GetEntry(node.store.LastIndex)
	node.send(NewAppendEntryMsg(m.Id, ent, prev))
}

func (node *Node)replicateAllMembers(){
	for _, m := range node.Members {
		node.replicateMember(m)
	}
	// 单节点运行
	if len(node.Members) == 0 {
		node.store.CommitEntry(node.store.LastIndex)
	}
}

func (node *Node)replicateMember(m *Member){
	if m.MatchIndex != 0 && m.NextIndex - m.MatchIndex > m.SendWindow {
		log.Printf("stop and wait %s, next: %d, match: %d", m.Id, m.NextIndex, m.MatchIndex)
		return
	}

	m.ReplicateTimer = 0
	maxIndex := util.MaxInt64(m.NextIndex, m.MatchIndex + m.SendWindow)
	for m.NextIndex <= maxIndex {
		ent := node.store.GetEntry(m.NextIndex)
		if ent == nil {
			break
		}
		ent.Commit = node.store.CommitIndex
		
		prev := node.store.GetEntry(m.NextIndex - 1)
		node.send(NewAppendEntryMsg(m.Id, ent, prev))
		
		m.NextIndex ++
		m.HeartbeatTimer = 0
	}
}

func (node *Node)addMember(nodeId string, nodeAddr string){
	if nodeId == node.Id {
		return
	}
	if node.Members[nodeId] != nil {
		return
	}
	m := NewMember(nodeId, nodeAddr)
	node.resetMember(m)
	node.Members[m.Id] = m
	log.Println("    add member", m.Id, m.Addr)
}

func (node *Node)disconnectAllMember(){
	for _, m := range node.Members {
		// it's ok to delete item while iterating
		node.removeMember(m.Id)
	}
}

func (node *Node)removeMember(nodeId string){
	if nodeId == node.Id {
		return
	}
	if node.Members[nodeId] == nil {
		return
	}
	m := node.Members[nodeId]
	delete(node.Members, nodeId)
	log.Println("    disconnect member", m.Id, m.Addr)
}

/* ############################################# */

func (node *Node)handleRaftMessage(msg *Message){
	if msg.Dst != node.Id || node.Members[msg.Src] == nil {
		log.Println(node.Id, "drop message from unknown src", msg.Src, "dst", msg.Dst, "members: ", node.Members)
		return
	}

	// MUST: smaller msg.Term is rejected or ignored
	if msg.Term < node.Term {
		log.Println("reject", msg.Type, "msg.Term =", msg.Term, " < node.term = ", node.Term)
		node.send(NewNoneMsg(msg.Src))
		// finish processing msg
		return
	}
	// MUST: node.Term is set to be larger msg.Term
	if msg.Term > node.Term {
		log.Printf("receive greater msg.term: %d, node.term: %d", msg.Term, node.Term)
		node.Term = msg.Term
		node.VoteFor = ""
		if node.Role != RoleFollower {
			log.Printf("Node %s became follower", node.Id)
			node.becomeFollower()
		}
		node.store.SaveState()
		// continue processing msg
	}
	if msg.Type == MessageTypeNone {
		return
	}

	if node.Role == RoleLeader {
		if msg.Type == MessageTypeAppendEntryAck {
			node.handleAppendEntryAck(msg)
		} else if msg.Type == MessageTypePreVote {
			node.handlePreVote(msg)
		} else {
			log.Println("drop message", msg.Encode())
		}
		return
	}
	if node.Role == RoleCandidate {
		if msg.Type == MessageTypeRequestVoteAck {
			node.handleRequestVoteAck(msg)
		} else {
			log.Println("drop message", msg.Encode())
		}
		return
	}
	if node.Role == RoleFollower {
		if msg.Type == MessageTypeRequestVote {
			node.handleRequestVote(msg)
		} else if msg.Type == MessageTypeAppendEntry {
			node.handleAppendEntry(msg)
		} else if msg.Type == MessageTypeInstallSnapshot {
			node.handleInstallSnapshot(msg)
		} else if msg.Type == MessageTypePreVote {
			node.handlePreVote(msg)
		} else if msg.Type == MessageTypePreVoteAck {
			node.handlePreVoteAck(msg)
		} else {
			log.Println("drop message", msg.Encode())
		}
		return
	}
}

func (node *Node)handlePreVote(msg *Message){
	if node.Role == RoleLeader {
		arr := make([]int, 0, len(node.Members) + 1)
		arr = append(arr, 0) // self
		for _, m := range node.Members {
			arr = append(arr, m.ReceiveTimeout)
		}
		sort.Ints(arr)
		log.Println("    receive timeouts =", arr)
		timer := arr[len(arr)/2]
		if timer < ReceiveTimeout {
			log.Println("    major followers are still reachable, ignore")
			return
		}
	}
	for _, m := range node.Members {
		if m.Role == RoleLeader && m.ReceiveTimeout < ReceiveTimeout {
			log.Printf("leader %s is still active, ignore PreVote from %s", m.Id, msg.Src)
			return
		}
	}
	node.send(NewPreVoteAck(msg.Src))
}

func (node *Node)handlePreVoteAck(msg *Message){
	log.Printf("receive PreVoteAck from %s", msg.Src)
	node.votesReceived[msg.Src] = msg.Data
	if len(node.votesReceived) + 1 > (len(node.Members) + 1)/2 {
		node.startElection()
	}
}

func (node *Node)handleRequestVote(msg *Message){
	// node.VoteFor == msg.Src: retransimitted/duplicated RequestVote
	if node.VoteFor != "" && node.VoteFor != msg.Src {
		// just ignore
		log.Println("already vote for", node.VoteFor, "ignore", msg.Src)
		return
	}
	
	granted := false
	if msg.PrevTerm > node.store.LastTerm {
		granted = true
	} else if msg.PrevTerm == node.store.LastTerm && msg.PrevIndex >= node.store.LastIndex {
		granted = true
	} else {
		// we've got newer log, reject
	}

	if granted {
		node.electionTimer = 0
		log.Println("vote for", msg.Src)
		node.VoteFor = msg.Src
		node.store.SaveState()
		node.send(NewRequestVoteAck(msg.Src, true))
	} else {
		node.send(NewRequestVoteAck(msg.Src, false))
	}
}

func (node *Node)handleRequestVoteAck(msg *Message){
	log.Printf("receive vote %s from %s", msg.Data, msg.Src)
	node.votesReceived[msg.Src] = msg.Data
	node.checkVoteResult()
}

func (node *Node)sendDuplicatedAckToMessage(msg *Message){
	var prev *Entry
	if msg.PrevIndex < node.store.LastIndex {
		prev = node.store.GetEntry(msg.PrevIndex - 1)
	} else {
		prev = node.store.GetEntry(node.store.LastIndex)
	}
	
	ack := NewAppendEntryAck(msg.Src, false)
	if prev != nil {
		ack.PrevTerm = prev.Term
		ack.PrevIndex = prev.Index
	}

	node.send(ack)
}

func (node *Node)handleAppendEntry(msg *Message){
	node.electionTimer = 0
	m := node.Members[msg.Src]
	m.Role = RoleLeader
	m.ReceiveTimeout = 0
	for _, m2 := range node.Members {
		if m != m2 {
			m2.Role = RoleFollower
		}
	}

	if msg.PrevIndex > node.store.CommitIndex {
		if msg.PrevIndex != node.store.LastIndex {
			log.Printf("non-continuous entry, prevIndex: %d, lastIndex: %d", msg.PrevIndex, node.store.LastIndex)
			node.sendDuplicatedAckToMessage(msg)
			return
		}
		prev := node.store.GetEntry(msg.PrevIndex)
		if prev == nil {
			log.Println("prev entry not found", msg.PrevTerm, msg.PrevIndex)
			node.sendDuplicatedAckToMessage(msg)
			return
		}
		if prev.Term != msg.PrevTerm {
			log.Printf("entry index: %d, prev.Term %d != msg.PrevTerm %d", msg.PrevIndex, prev.Term, msg.PrevTerm)
			node.sendDuplicatedAckToMessage(msg)
			return
		}
	}

	ent := DecodeEntry(msg.Data)

	if ent.Type == EntryTypePing {
		node.send(NewAppendEntryAck(msg.Src, true))
	} else {
		if ent.Index < node.store.CommitIndex {
			log.Printf("entry: %d before committed: %d", ent.Index, node.store.CommitIndex)
			node.sendDuplicatedAckToMessage(msg)
			return
		}

		old := node.store.GetEntry(ent.Index)
		if old != nil {
			if old.Term != ent.Term {
				// TODO:
				log.Println("TODO: delete conflict entry, and entries that follow")
			} else {
				log.Println("duplicated entry ", ent.Term, ent.Index)
			}
		}
		node.store.WriteEntry(*ent)
		// TODO: delay/batch ack
		node.send(NewAppendEntryAck(msg.Src, true))
	}

	node.store.CommitEntry(ent.Commit)
}

func (node *Node)handleAppendEntryAck(msg *Message){
	m := node.Members[msg.Src]
	m.ReceiveTimeout = 0

	if msg.Data == "false" {
		log.Printf("node %s, reset nextIndex: %d -> %d", m.Id, m.NextIndex, msg.PrevIndex + 1)
		m.NextIndex = msg.PrevIndex + 1
	} else {
		m.MatchIndex = util.MaxInt64(m.MatchIndex, msg.PrevIndex)
		m.NextIndex  = util.MaxInt64(m.NextIndex, m.MatchIndex + 1)
		if m.MatchIndex > node.store.CommitIndex {
			commitIndex := node.checkCommitIndex()
			if commitIndex > node.store.CommitIndex {
				// only commit currentTerm's log
				ent := node.store.GetEntry(commitIndex)
				if ent.Term == node.Term {
					node.store.CommitEntry(commitIndex)

					// follower acked last entry, heartbeat it to commit
					if m.MatchIndex == node.store.LastIndex {
						// TODO: ping all?
						node.pingMember(m)
						return
					}
				}
			}
		}
	}

	// force new node added to group to install snapshot, avoid replaying too many logs.
	if msg.PrevIndex == 0 {
		log.Printf("new node, notify it to install snapshot")
		node.sendInstallSnapshot(m)
		return
	}
	if m.NextIndex < node.store.FirstIndex {
		log.Printf("follower %s out-of-sync, notify it to install snapshot", m.Id)
		node.sendInstallSnapshot(m)
		return
	}
	node.replicateMember(m)
}

func (node *Node)checkCommitIndex() int64 {
	// sort matchIndex[] in descend order
	matchIndex := make([]int64, 0, len(node.Members) + 1)
	matchIndex = append(matchIndex, node.store.LastIndex) // self
	for _, m := range node.Members {
		matchIndex = append(matchIndex, m.MatchIndex)
	}
	sort.Slice(matchIndex, func(i, j int) bool{
		return matchIndex[i] > matchIndex[j]
	})
	commitIndex := matchIndex[len(matchIndex)/2]
	log.Println("match[] =", matchIndex, "commit", commitIndex)
	return commitIndex
}

func (node *Node)sendInstallSnapshot(m *Member){
	sn := node.store.CreateSnapshot()
	if sn == nil {
		log.Println("CreateSnapshot() error!")
		return
	}
	msg := NewInstallSnapshotMsg(m.Id, sn.Encode())
	node.send(msg)
}

func (node *Node)handleInstallSnapshot(msg *Message){
	sn := NewSnapshotFromString(msg.Data)
	if sn == nil {
		log.Println("NewSnapshotFromString() error!")
		return
	}
	node._installSnapshot(sn)
	node.send(NewAppendEntryAck(msg.Src, true))
	
	// TODO: notify service to install snapshot
	log.Println("TODO: install Service snapshot")
}

func (node *Node)_installSnapshot(sn *Snapshot) bool {
	log.Println("install Raft snapshot")
	node.disconnectAllMember()
	for nodeId, nodeAddr := range sn.State().Members {
		node.addMember(nodeId, nodeAddr)
	}
	node.lastApplied = sn.LastIndex()

	return node.store.InstallSnapshot(sn)
}

/* ###################### Service interface ####################### */

func (node *Node)LastApplied() int64{
	return node.lastApplied
}

func (node *Node)ApplyEntry(ent *Entry){
	node.lastApplied = ent.Index

	// 注意, 不能在 ApplyEntry 里修改 CommitIndex
	if ent.Type == EntryTypeAddMember {
		log.Println("[Apply]", ent.Encode())
		ps := strings.Split(ent.Data, " ")
		if len(ps) == 2 {
			node.addMember(ps[0], ps[1])
			node.store.SaveState()
		}
	}else if ent.Type == EntryTypeDelMember {
		log.Println("[Apply]", ent.Encode())
		nodeId := ent.Data
		// the deleted node would not receive a commit msg that it had been deleted
		node.removeMember(nodeId)
		node.store.SaveState()
	}
}

/* ###################### Quorum Methods ####################### */

func (node *Node)AddMember(nodeId string, nodeAddr string) int64 {
	node.mux.Lock()
	defer node.mux.Unlock()

	if node.Role != RoleLeader {
		if len(node.Members) == 0 {
			// TODO: init state from storage
			node.becomeLeader();
		} else {
			log.Println("error: not leader")
			return -1
		}
	}

	data := fmt.Sprintf("%s %s", nodeId, nodeAddr)
	ent := node.store.AppendEntry(EntryTypeAddMember, data)
	return ent.Index
}

func (node *Node)DelMember(nodeId string) int64 {
	node.mux.Lock()
	defer node.mux.Unlock()

	if node.Role != RoleLeader {
		log.Println("error: not leader")
		return -1
	}
	
	data := nodeId
	ent := node.store.AppendEntry(EntryTypeDelMember, data)
	return ent.Index
}

func (node *Node)Propose(data string) (int32, int64) {
	node.mux.Lock()
	defer node.mux.Unlock()
	
	log.Println("")
	if node.Role != RoleLeader {
		log.Println("error: not leader")
		return -1, -1
	}
	
	ent := node.store.AppendEntry(EntryTypeData, data)
	return ent.Term, ent.Index
}

/* ###################### Operations ####################### */

func (node *Node)InfoMap() map[string]string {
	node.mux.Lock()
	defer node.mux.Unlock()
	
	m := make(map[string]string)
	m["id"] = fmt.Sprintf("%s", node.Id)
	m["addr"] = node.Addr
	m["role"] = string(node.Role)
	m["term"] = fmt.Sprintf("%d", node.Term)
	m["voteFor"] = fmt.Sprintf("%s", node.VoteFor)
	m["lastApplied"] = fmt.Sprintf("%d", node.lastApplied)
	m["commitIndex"] = fmt.Sprintf("%d", node.store.CommitIndex)
	m["lastTerm"] = fmt.Sprintf("%d", node.store.LastTerm)
	m["lastIndex"] = fmt.Sprintf("%d", node.store.LastIndex)
	b, _ := json.Marshal(node.Members)
	m["members"] = string(b)
	return m
}

func (node *Node)Info() string {
	node.mux.Lock()
	defer node.mux.Unlock()
	
	var ret string
	ret += fmt.Sprintf("id: %s\n", node.Id)
	ret += fmt.Sprintf("addr: %s\n", node.Addr)
	ret += fmt.Sprintf("role: %s\n", node.Role)
	ret += fmt.Sprintf("term: %d\n", node.Term)
	ret += fmt.Sprintf("voteFor: %s\n", node.VoteFor)
	ret += fmt.Sprintf("lastApplied: %d\n", node.lastApplied)
	ret += fmt.Sprintf("commitIndex: %d\n", node.store.CommitIndex)
	ret += fmt.Sprintf("lastTerm: %d\n", node.store.LastTerm)
	ret += fmt.Sprintf("lastIndex: %d\n", node.store.LastIndex)
	ret += fmt.Sprintf("electionTimer: %d\n", node.electionTimer)
	b, _ := json.Marshal(node.Members)
	ret += fmt.Sprintf("members: %s\n", string(b))

	return ret
}

func (node *Node)CreateSnapshot() *Snapshot {
	node.mux.Lock()
	defer node.mux.Unlock()
	
	return node.store.CreateSnapshot()
}

func (node *Node)InstallSnapshot(sn *Snapshot) bool {
	node.mux.Lock()
	defer node.mux.Unlock()
	
	return node._installSnapshot(sn)
}

func (node *Node)JoinGroup(leaderId string, leaderAddr string) {
	node.mux.Lock()
	defer node.mux.Unlock()
	
	if leaderId == node.Id {
		log.Println("could not join self:", leaderId)
		return
	}
	if len(node.Members) > 0 {
		log.Println("already in group")
		return
	}
	log.Println("JoinGroup", leaderId, leaderAddr)

	node.Term = 0
	node.VoteFor = ""
	node.lastApplied = 0
	node.addMember(leaderId, leaderAddr)
	node.becomeFollower()
	
	log.Println("clean Raft database")
	node.store.CleanAll()
}

func (node *Node)QuitGroup() {
	node.mux.Lock()
	defer node.mux.Unlock()
	
	log.Println("QuitGroup")
	node.disconnectAllMember()
	node.store.SaveState()
}


/* ############################################# */

func (node *Node)send(msg *Message){
	msg.Src = node.Id
	msg.Term = node.Term
	if msg.PrevTerm == 0 {
		msg.PrevTerm = node.store.LastTerm
		msg.PrevIndex = node.store.LastIndex
	}
	node.send_c <- msg
}

func (node *Node)broadcast(msg *Message){
	for _, m := range node.Members {
		msg.Dst = m.Id
		node.send(msg)
	}
}
