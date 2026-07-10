package ui

import (
	"testing"

	"github.com/tpenzkofer/kubeview/internal/cluster"
)

func treeModel() Model {
	pods := []cluster.PodInfo{
		{Namespace: "default", Name: "web-1", Controller: "Deployment/web", Total: 1, Ready: 1},
		{Namespace: "default", Name: "web-2", Controller: "Deployment/web", Total: 1, Ready: 1},
		{Namespace: "default", Name: "api-1", Controller: "Deployment/api", Total: 1, Ready: 1},
		{Namespace: "kube-system", Name: "coredns-1", Controller: "Deployment/coredns", Total: 1, Ready: 1},
	}
	m := New(nil, 0, "")
	m.snap = cluster.Snapshot{Pods: pods}
	m.tree = true
	m.applyFilter()
	return m
}

func kinds(m Model) []treeRowKind {
	rows := m.treeRows()
	out := make([]treeRowKind, len(rows))
	for i, r := range rows {
		out[i] = r.kind
	}
	return out
}

func TestTreeRowsNestNamespaceWorkloadPod(t *testing.T) {
	m := treeModel()
	// default(ns) > api(wl) > api-1 > web(wl) > web-1 > web-2 > kube-system(ns) > coredns(wl) > coredns-1
	want := []treeRowKind{rowNS, rowGroup, rowPod, rowGroup, rowPod, rowPod, rowNS, rowGroup, rowPod}
	got := kinds(m)
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d kind = %v, want %v", i, got[i], want[i])
		}
	}
}

// The bug this guards: folding a node used to move the selection away from it,
// leaving nothing on screen that could unfold it again.
func TestFoldedNodeStaysSelectedAndUnfolds(t *testing.T) {
	m := treeModel()
	m.treeSel = nsKey("default")

	m.foldNode(nsKey("default"))
	if !m.collapsed[nsKey("default")] {
		t.Fatal("namespace did not fold")
	}
	if r, _ := m.currentTreeRow(); r.key != nsKey("default") {
		t.Fatalf("selection moved off the folded node: %q", r.key)
	}

	m.foldNode(nsKey("default")) // the very same key must reopen it
	if m.collapsed[nsKey("default")] {
		t.Fatal("namespace did not unfold")
	}
	if n := len(m.treeRows()); n != 9 {
		t.Fatalf("after unfolding got %d rows, want 9", n)
	}
}

// Folding a pod's workload selects that workload, so space is symmetric.
func TestFoldFromPodRowSelectsTheWorkload(t *testing.T) {
	m := treeModel()
	m.treeSel = podKey(cluster.PodInfo{Namespace: "default", Name: "web-1"})

	r, ok := m.currentTreeRow()
	if !ok || r.kind != rowPod {
		t.Fatalf("expected a pod row, got %+v", r)
	}
	m.foldNode(groupKey(m.rows[r.pod]))

	r, _ = m.currentTreeRow()
	if r.kind != rowGroup {
		t.Fatalf("selection kind = %v, want rowGroup", r.kind)
	}
	if r.key != "default\x00Deployment/web" {
		t.Fatalf("selected %q, want the web workload", r.key)
	}
}

// Every namespace folded: the tree is one row per namespace and still navigable.
func TestFoldAllNamespacesLeavesNamespaceRows(t *testing.T) {
	m := treeModel()
	m.setAllNamespacesCollapsed(true)
	for _, r := range m.treeRows() {
		if r.kind != rowNS {
			t.Fatalf("unexpected row kind %v with all namespaces folded", r.kind)
		}
	}
	if n := len(m.treeRows()); n != 2 {
		t.Fatalf("got %d namespace rows, want 2", n)
	}
	m.treeSel = nsKey("default")
	m.moveTree(1)
	if r, _ := m.currentTreeRow(); r.key != nsKey("kube-system") {
		t.Fatalf("moveTree landed on %q, want kube-system", r.key)
	}
}

// A header row must never hand a pod to a destructive action.
func TestActionPodRefusesHeaderRows(t *testing.T) {
	m := treeModel()
	m.treeSel = nsKey("default")
	if _, ok := m.actionPod(); ok {
		t.Fatal("actionPod returned a pod while a namespace header was selected")
	}
	if m.status == "" {
		t.Fatal("actionPod should explain why it refused")
	}

	m.treeSel = podKey(cluster.PodInfo{Namespace: "default", Name: "api-1"})
	if _, ok := m.actionPod(); !ok {
		t.Fatal("actionPod refused a pod row")
	}
}
