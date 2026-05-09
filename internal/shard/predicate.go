// Package shard implements controller sharding for horizontal scaling.
// Inspired by Kargo's pkg/controller/predicates.go — uses a label-based
// predicate to partition objects across controller replicas.
//
// When KAPRO_SHARD is empty, all objects are processed (backward compatible).
// When KAPRO_SHARD="shard-1", only objects with label kapro.io/shard=shard-1
// are processed. Objects without the shard label are processed by the default
// shard (IsDefault=true).
package shard

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// LabelShard is the label key used to assign objects to a shard.
	LabelShard = "kapro.io/shard"
)

// ShardFilter is a predicate.Predicate that filters objects by shard label.
// When ShardName is empty, all objects pass (no sharding).
type ShardFilter struct {
	// ShardName is the name of this shard. When empty, all objects pass.
	ShardName string
	// IsDefault controls whether unlabeled objects are processed by this shard.
	// Only one shard should have IsDefault=true.
	IsDefault bool
}

// compile-time check
var _ predicate.Predicate = ShardFilter{}

// Filter returns true if the object should be processed by this shard.
func (f ShardFilter) Filter(obj client.Object) bool {
	// No sharding configured — process everything.
	if f.ShardName == "" {
		return true
	}
	objShard := obj.GetLabels()[LabelShard]
	// Object is assigned to this shard.
	if objShard == f.ShardName {
		return true
	}
	// Object has no shard label and this is the default shard.
	if objShard == "" && f.IsDefault {
		return true
	}
	return false
}

// Create implements predicate.Predicate.
func (f ShardFilter) Create(e event.CreateEvent) bool {
	if e.Object == nil {
		return false
	}
	return f.Filter(e.Object)
}

// Update implements predicate.Predicate.
func (f ShardFilter) Update(e event.UpdateEvent) bool {
	if e.ObjectNew == nil {
		return false
	}
	return f.Filter(e.ObjectNew)
}

// Delete implements predicate.Predicate.
func (f ShardFilter) Delete(e event.DeleteEvent) bool {
	if e.Object == nil {
		return false
	}
	return f.Filter(e.Object)
}

// Generic implements predicate.Predicate.
func (f ShardFilter) Generic(e event.GenericEvent) bool {
	if e.Object == nil {
		return false
	}
	return f.Filter(e.Object)
}
