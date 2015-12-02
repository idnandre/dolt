// This file was generated by a slightly modified nomdl/codegen
// To generate this I added support for `Package` in the NomDL parser (Just add `Package` after `Uint32` etc)

package types

import (
	"github.com/attic-labs/noms/chunks"
	"github.com/attic-labs/noms/ref"
)

// SetOfRefOfPackage

type SetOfRefOfPackage struct {
	s   Set
	ref *ref.Ref
	cs  chunks.ChunkStore
}

func NewSetOfRefOfPackage(cs chunks.ChunkStore) SetOfRefOfPackage {
	return SetOfRefOfPackage{NewTypedSet(cs, __typeForSetOfRefOfPackage), &ref.Ref{}, cs}
}

type SetOfRefOfPackageDef map[ref.Ref]bool

func (def SetOfRefOfPackageDef) New(cs chunks.ChunkStore) SetOfRefOfPackage {
	l := make([]Value, len(def))
	i := 0
	for d, _ := range def {
		l[i] = NewRefOfPackage(d)
		i++
	}
	return SetOfRefOfPackage{NewTypedSet(cs, __typeForSetOfRefOfPackage, l...), &ref.Ref{}, cs}
}

func (s SetOfRefOfPackage) Def() SetOfRefOfPackageDef {
	def := make(map[ref.Ref]bool, s.Len())
	s.s.Iter(func(v Value) bool {
		def[v.(RefOfPackage).TargetRef()] = true
		return false
	})
	return def
}

func (s SetOfRefOfPackage) Equals(other Value) bool {
	return other != nil && __typeForSetOfRefOfPackage.Equals(other.Type()) && s.Ref() == other.Ref()
}

func (s SetOfRefOfPackage) Ref() ref.Ref {
	return EnsureRef(s.ref, s)
}

func (s SetOfRefOfPackage) Chunks() (chunks []ref.Ref) {
	chunks = append(chunks, s.Type().Chunks()...)
	chunks = append(chunks, s.s.Chunks()...)
	return
}

func (s SetOfRefOfPackage) ChildValues() (ret []Value) {
	ret = append(ret, s.Type())
	ret = append(ret, s.s.ChildValues()...)
	return
}

// A Noms Value that describes SetOfRefOfPackage.
var __typeForSetOfRefOfPackage Type

func (m SetOfRefOfPackage) Type() Type {
	return __typeForSetOfRefOfPackage
}

func init() {
	__typeForSetOfRefOfPackage = MakeCompoundType(SetKind, MakeCompoundType(RefKind, MakePrimitiveType(PackageKind)))
	RegisterValue(__typeForSetOfRefOfPackage, builderForSetOfRefOfPackage, readerForSetOfRefOfPackage)
}

func builderForSetOfRefOfPackage(cs chunks.ChunkStore, v Value) Value {
	return SetOfRefOfPackage{v.(Set), &ref.Ref{}, cs}
}

func readerForSetOfRefOfPackage(v Value) Value {
	return v.(SetOfRefOfPackage).s
}

func (s SetOfRefOfPackage) Empty() bool {
	return s.s.Empty()
}

func (s SetOfRefOfPackage) Len() uint64 {
	return s.s.Len()
}

func (s SetOfRefOfPackage) Has(p RefOfPackage) bool {
	return s.s.Has(p)
}

type SetOfRefOfPackageIterCallback func(p RefOfPackage) (stop bool)

func (s SetOfRefOfPackage) Iter(cb SetOfRefOfPackageIterCallback) {
	s.s.Iter(func(v Value) bool {
		return cb(v.(RefOfPackage))
	})
}

type SetOfRefOfPackageIterAllCallback func(p RefOfPackage)

func (s SetOfRefOfPackage) IterAll(cb SetOfRefOfPackageIterAllCallback) {
	s.s.IterAll(func(v Value) {
		cb(v.(RefOfPackage))
	})
}

type SetOfRefOfPackageFilterCallback func(p RefOfPackage) (keep bool)

func (s SetOfRefOfPackage) Filter(cb SetOfRefOfPackageFilterCallback) SetOfRefOfPackage {
	ns := NewSetOfRefOfPackage(s.cs)
	s.IterAll(func(v RefOfPackage) {
		if cb(v) {
			ns = ns.Insert(v)
		}
	})
	return ns
}

func (s SetOfRefOfPackage) Insert(p ...RefOfPackage) SetOfRefOfPackage {
	return SetOfRefOfPackage{s.s.Insert(s.fromElemSlice(p)...), &ref.Ref{}, s.cs}
}

func (s SetOfRefOfPackage) Remove(p ...RefOfPackage) SetOfRefOfPackage {
	return SetOfRefOfPackage{s.s.Remove(s.fromElemSlice(p)...), &ref.Ref{}, s.cs}
}

func (s SetOfRefOfPackage) Union(others ...SetOfRefOfPackage) SetOfRefOfPackage {
	return SetOfRefOfPackage{s.s.Union(s.fromStructSlice(others)...), &ref.Ref{}, s.cs}
}

func (s SetOfRefOfPackage) Subtract(others ...SetOfRefOfPackage) SetOfRefOfPackage {
	return SetOfRefOfPackage{s.s.Subtract(s.fromStructSlice(others)...), &ref.Ref{}, s.cs}
}

func (s SetOfRefOfPackage) Any() RefOfPackage {
	return s.s.Any().(RefOfPackage)
}

func (s SetOfRefOfPackage) fromStructSlice(p []SetOfRefOfPackage) []Set {
	r := make([]Set, len(p))
	for i, v := range p {
		r[i] = v.s
	}
	return r
}

func (s SetOfRefOfPackage) fromElemSlice(p []RefOfPackage) []Value {
	r := make([]Value, len(p))
	for i, v := range p {
		r[i] = v
	}
	return r
}

// RefOfPackage

type RefOfPackage struct {
	target ref.Ref
	ref    *ref.Ref
}

func NewRefOfPackage(target ref.Ref) RefOfPackage {
	return RefOfPackage{target, &ref.Ref{}}
}

func (r RefOfPackage) TargetRef() ref.Ref {
	return r.target
}

func (r RefOfPackage) Ref() ref.Ref {
	return EnsureRef(r.ref, r)
}

func (r RefOfPackage) Equals(other Value) bool {
	return other != nil && __typeForRefOfPackage.Equals(other.Type()) && r.Ref() == other.Ref()
}

func (r RefOfPackage) Chunks() (chunks []ref.Ref) {
	chunks = append(chunks, r.Type().Chunks()...)
	chunks = append(chunks, r.target)
	return
}

func (r RefOfPackage) ChildValues() []Value {
	return nil
}

// A Noms Value that describes RefOfPackage.
var __typeForRefOfPackage Type

func (m RefOfPackage) Type() Type {
	return __typeForRefOfPackage
}

func init() {
	__typeForRefOfPackage = MakeCompoundType(RefKind, MakePrimitiveType(PackageKind))
	RegisterRef(__typeForRefOfPackage, builderForRefOfPackage)
}

func builderForRefOfPackage(r ref.Ref) Value {
	return NewRefOfPackage(r)
}

func (r RefOfPackage) TargetValue(cs chunks.ChunkStore) Package {
	return ReadValue(r.target, cs).(Package)
}

func (r RefOfPackage) SetTargetValue(val Package, cs chunks.ChunkSink) RefOfPackage {
	return NewRefOfPackage(WriteValue(val, cs))
}
