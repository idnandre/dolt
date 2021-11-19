// Copyright 2020 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dtables

import (
	"context"
	"strings"
	"sync"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"

	"github.com/dolthub/dolt/go/libraries/doltcore/schema"
	"github.com/dolthub/dolt/go/libraries/doltcore/sqle/expreval"
	"github.com/dolthub/dolt/go/libraries/doltcore/sqle/setalgebra"
	"github.com/dolthub/dolt/go/libraries/doltcore/table"
	"github.com/dolthub/dolt/go/libraries/doltcore/table/typed/noms"
	"github.com/dolthub/dolt/go/store/hash"
	"github.com/dolthub/dolt/go/store/types"
)

type createSetFunc func(nbf *types.NomsBinFormat, val types.Value) (setalgebra.Set, error)

func setForEqOp(nbf *types.NomsBinFormat, val types.Value) (setalgebra.Set, error) {
	return setalgebra.NewFiniteSet(nbf, val)
}

func setForGtOp(nbf *types.NomsBinFormat, val types.Value) (setalgebra.Set, error) {
	return setalgebra.NewInterval(nbf, &setalgebra.IntervalEndpoint{Val: val, Inclusive: false}, nil), nil
}

func setForGteOp(nbf *types.NomsBinFormat, val types.Value) (setalgebra.Set, error) {
	return setalgebra.NewInterval(nbf, &setalgebra.IntervalEndpoint{Val: val, Inclusive: true}, nil), nil
}

func setForLtOp(nbf *types.NomsBinFormat, val types.Value) (setalgebra.Set, error) {
	return setalgebra.NewInterval(nbf, nil, &setalgebra.IntervalEndpoint{Val: val, Inclusive: false}), nil
}

func setForLteOp(nbf *types.NomsBinFormat, val types.Value) (setalgebra.Set, error) {
	return setalgebra.NewInterval(nbf, nil, &setalgebra.IntervalEndpoint{Val: val, Inclusive: true}), nil
}

// getSetForKeyColumn takes a filter, and a single key column (schemas with multiple columns used as keys is not
// supported yet), and returns a set which represents all the values which satisfies the filter for the given key
// column
func getSetForKeyColumn(nbf *types.NomsBinFormat, col schema.Column, filter sql.Expression) (setalgebra.Set, error) {
	switch typedExpr := filter.(type) {
	case *expression.Or:
		return getSetForOrExpression(nbf, col, typedExpr.Left, typedExpr.Right)
	case *expression.And:
		return getSetForAndExpression(nbf, col, typedExpr.Left, typedExpr.Right)
	case *expression.Equals:
		eqOp := expreval.EqualsOp{}
		return setForComparisonExp(nbf, col, typedExpr.BinaryExpression, eqOp, setForEqOp)
	case *expression.GreaterThan:
		gtOp := expreval.GreaterOp{NBF: nbf}
		return setForComparisonExp(nbf, col, typedExpr.BinaryExpression, gtOp, setForGtOp)
	case *expression.GreaterThanOrEqual:
		gteOp := expreval.GreaterEqualOp{NBF: nbf}
		return setForComparisonExp(nbf, col, typedExpr.BinaryExpression, gteOp, setForGteOp)
	case *expression.LessThan:
		ltOp := expreval.LessOp{NBF: nbf}
		return setForComparisonExp(nbf, col, typedExpr.BinaryExpression, ltOp, setForLtOp)
	case *expression.LessThanOrEqual:
		lteOp := expreval.LessEqualOp{NBF: nbf}
		return setForComparisonExp(nbf, col, typedExpr.BinaryExpression, lteOp, setForLteOp)
	case *expression.InTuple:
		return setForInExp(nbf, col, typedExpr.BinaryExpression)
		// case *expression.Subquery:
	}

	return setalgebra.UniversalSet{}, nil
}

// getSetForOrExpression gets the set of values which satisfies the or condition.  This will be the union of
// the sets generated by the left and right sides of the or expression.
func getSetForOrExpression(nbf *types.NomsBinFormat, col schema.Column, left, right sql.Expression) (setalgebra.Set, error) {
	leftSet, err := getSetForKeyColumn(nbf, col, left)

	if err != nil {
		return nil, err
	}

	rightSet, err := getSetForKeyColumn(nbf, col, right)

	if err != nil {
		return nil, err
	}

	return leftSet.Union(rightSet)
}

// getSetForAndExpression gets the set of values which satisfies the and condition.  This will be the intersection of
// the sets generated by the left and right sides of the and expression.
func getSetForAndExpression(nbf *types.NomsBinFormat, col schema.Column, left, right sql.Expression) (setalgebra.Set, error) {
	leftSet, err := getSetForKeyColumn(nbf, col, left)

	if err != nil {
		return nil, err
	}

	rightSet, err := getSetForKeyColumn(nbf, col, right)

	if err != nil {
		return nil, err
	}

	return leftSet.Intersect(rightSet)
}

// setForComparisonExp returns the set of values which satisfies the comparison expression for the specified column.
func setForComparisonExp(
	nbf *types.NomsBinFormat,
	col schema.Column,
	be expression.BinaryExpression,
	op expreval.CompareOp,
	createSet createSetFunc) (setalgebra.Set, error) {

	variables, literals, compType, err := expreval.GetComparisonType(be)

	if err != nil {
		return nil, err
	}

	switch compType {
	case expreval.ConstConstCompare:
		res, err := op.CompareLiterals(literals[0], literals[1])

		if err != nil {
			return nil, err
		}

		// Comparing literals will have the same result for all columns.  When true it does not limit
		// the range of keys and the universal set is returned.  If it is false then no value can
		// satisfy the condition and the empty set is returned.
		if res {
			return setalgebra.UniversalSet{}, nil
		} else {
			return setalgebra.EmptySet{}, nil
		}

	case expreval.VariableConstCompare:
		// check to see if this comparison is with the primary key column
		if strings.EqualFold(variables[0].Name(), col.Name) {
			val, err := expreval.LiteralToNomsValue(col.Kind, literals[0])

			if err != nil {
				return nil, err
			}

			// create the set for the keys matching the condition
			return createSet(nbf, val)
		} else {
			// not the primary key column, so this will not limit the set of key values, so the universal set is returned.
			return setalgebra.UniversalSet{}, nil
		}

	case expreval.VariableVariableCompare:
		// In the case where one columns value is being compared to another we cannot limit the keys we look at.
		// We need to read the entire row in before filtering.
		return setalgebra.UniversalSet{}, nil
	}

	panic("Unexpected case value")
}

// setForComparisonExp returns the set of values which satisfies the set membership test for the specified column.
func setForInExp(nbf *types.NomsBinFormat, col schema.Column, be expression.BinaryExpression) (setalgebra.Set, error) {
	variables, literals, compType, err := expreval.GetComparisonType(be)

	if err != nil || compType != expreval.VariableInLiteralList {
		// Unsupported in will not limit key set
		return setalgebra.UniversalSet{}, nil
	}

	// check to see if this comparison is with the primary key column
	if strings.EqualFold(variables[0].Name(), col.Name) {
		vals := make([]types.Value, len(literals))
		for i, literal := range literals {
			val, err := expreval.LiteralToNomsValue(col.Kind, literal)

			if err != nil {
				return nil, err
			}

			vals[i] = val
		}

		// not the primary key column, so this will not limit the set of key values, so the universal set is returned.
		return setalgebra.NewFiniteSet(nbf, vals...)
	}

	return setalgebra.UniversalSet{}, nil
}

// CreateReaderFunc creates a new instance of an object implementing TableReadCloser for the given types.Map.  The
// readers created by calling this function will have the rows they return limited by a setalgebra.Set implementation.
type CreateReaderFunc func(ctx context.Context, m types.Map) (table.TableReadCloser, error)

// ThreadSafeCRFuncCache is a thread safe CreateReaderFunc cache
type ThreadSafeCRFuncCache struct {
	funcs map[hash.Hash]CreateReaderFunc
	mu    *sync.Mutex
}

// NewThreadSafeCRFuncCache creates a new ThreadSafeCRFuncCache
func NewThreadSafeCRFuncCache() *ThreadSafeCRFuncCache {
	return &ThreadSafeCRFuncCache{make(map[hash.Hash]CreateReaderFunc), &sync.Mutex{}}
}

// GetOrCreate gets a CreateReaderFunc for the given hash if it exists in the cache.  If it doesn't it creates it and
// caches it.
func (cache *ThreadSafeCRFuncCache) GetOrCreate(h hash.Hash, nbf *types.NomsBinFormat, tblSch schema.Schema, filters []sql.Expression) (CreateReaderFunc, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	f, ok := cache.funcs[h]

	if ok {
		return f, nil
	}

	f, err := CreateReaderFuncLimitedByExpressions(nbf, tblSch, filters)

	if err != nil {
		return nil, err
	}

	cache.funcs[h] = f
	return f, nil
}

// CreateReaderFuncLimitedByExpressions takes a table schema and a slice of sql filters and returns a CreateReaderFunc
// which limits the rows read based on the filters supplied.
func CreateReaderFuncLimitedByExpressions(nbf *types.NomsBinFormat, tblSch schema.Schema, filters []sql.Expression) (CreateReaderFunc, error) {
	pkCols := tblSch.GetPKCols()
	var keySet setalgebra.Set = setalgebra.UniversalSet{}
	var err error
	for _, filter := range filters {
		var setForFilter setalgebra.Set
		setForFilter, err = getSetForKeyColumn(nbf, pkCols.GetByIndex(0), filter)

		if err != nil {
			break
		}

		keySet, err = keySet.Intersect(setForFilter)

		if err != nil {
			break
		}
	}

	if err != nil {
		// should probably log this to some debug logger. don't fail, just fall back on a full table
		// scan.
	} else {
		return getCreateFuncForKeySet(nbf, keySet, tblSch)
	}

	return func(ctx context.Context, m types.Map) (table.TableReadCloser, error) {
		return noms.NewNomsMapReader(ctx, m, tblSch)
	}, nil
}

// finiteSetToKeySlice takes a setalgebra.FiniteSet instance and converts it to a slice of types.Tuple which can
// be used as the keys when reading from a types.Map
func finiteSetToKeySlice(nbf *types.NomsBinFormat, tag types.Uint, fs setalgebra.FiniteSet) ([]types.Tuple, error) {
	keys := make([]types.Tuple, len(fs.HashToVal))

	i := 0
	var err error
	for _, v := range fs.HashToVal {
		keys[i], err = types.NewTuple(nbf, tag, v)
		i++

		if err != nil {
			return nil, err
		}
	}

	return keys, nil
}

func rangesForFiniteSetOfPartialKeys(nbf *types.NomsBinFormat, tag types.Uint, fs setalgebra.FiniteSet) ([]*noms.ReadRange, error) {
	keys, err := finiteSetToKeySlice(nbf, tag, fs)

	if err != nil {
		return nil, err
	}

	ranges := make([]*noms.ReadRange, len(keys))
	for i, key := range keys {
		firstPKElemVal, err := key.Get(1)

		if err != nil {
			return nil, err
		}

		ranges[i] = &noms.ReadRange{Start: key, Inclusive: true, Reverse: false, Check: checkEquals{firstPKElemVal}}
	}

	return ranges, nil
}

// rangeForInterval converts a setalgebra.Interval into a noms.ReadRange for reading the values within the interval
// from a types.Map
func rangeForInterval(nbf *types.NomsBinFormat, tag types.Uint, in setalgebra.Interval) (*noms.ReadRange, error) {
	var inclusive bool
	var startKey types.Tuple
	var reverse bool
	var check noms.InRangeCheck

	// Has a start point so will iterate forward
	if in.Start != nil {
		var err error
		if !in.Start.Inclusive {
			startKey, err = types.NewTuple(nbf, tag, in.Start.Val, types.Uint(uint64(0xffffffffffffffff)))
		} else {
			startKey, err = types.NewTuple(nbf, tag, in.Start.Val)
		}

		inclusive = true

		if err != nil {
			return nil, err
		}

		// no upper bound so the check will always return true
		if in.End == nil {
			check = noms.InRangeCheckAlways{}
		} else if in.End.Inclusive {
			// has an endpoint which is inclusive of the endpoint value.  Return true while the value is less than or
			// equal to the endpoint
			check = checkLessThanOrEquals{in.End.Val}
		} else {
			// has an endpoint which is exclusive of the endpoint value.  Return true while the value is less than
			// the endpoint
			check = checkLessThan{in.End.Val}
		}
	} else {
		// No starting point so start at the end point and iterate backwards
		inclusive = in.End.Inclusive
		reverse = true

		var err error
		if inclusive {
			startKey, err = types.NewTuple(nbf, tag, in.End.Val, types.Uint(uint64(0xffffffffffffffff)))
		} else {
			startKey, err = types.NewTuple(nbf, tag, in.End.Val)
		}

		if err != nil {
			return nil, err
		}

		// there is no lower bound so always return true
		check = noms.InRangeCheckAlways{}
	}

	return &noms.ReadRange{Start: startKey, Inclusive: inclusive, Reverse: reverse, Check: check}, nil
}

// getCreateFuncForKeySet returns the CreateReaderFunc for a given setalgebra.Set which specifies which keys should
// be returned.
func getCreateFuncForKeySet(nbf *types.NomsBinFormat, keySet setalgebra.Set, sch schema.Schema) (CreateReaderFunc, error) {
	switch keySet.(type) {
	case setalgebra.EmptySet:
		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
			return noms.NewNomsMapReaderForKeys(m, sch, []types.Tuple{}), nil
		}, nil

	case setalgebra.UniversalSet:
		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
			return noms.NewNomsMapReader(ctx, m, sch)
		}, nil
	}

	pkCols := sch.GetPKCols()
	if pkCols.Size() == 1 {
		return getCreateFuncForSinglePK(nbf, keySet, sch)
	} else {
		return getCreateFuncForMultiPK(nbf, keySet, sch)
	}
}
func getCreateFuncForMultiPK(nbf *types.NomsBinFormat, keySet setalgebra.Set, sch schema.Schema) (CreateReaderFunc, error) {
	pkCols := sch.GetPKCols()
	col := pkCols.GetByIndex(0)
	switch typedSet := keySet.(type) {
	case setalgebra.FiniteSet:
		ranges, err := rangesForFiniteSetOfPartialKeys(nbf, types.Uint(col.Tag), typedSet)

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {

			var readers = []table.TableReadCloser{noms.NewNomsRangeReader(sch, m, ranges)}
			return table.NewCompositeTableReader(readers)
		}, nil

	case setalgebra.Interval:
		r, err := rangeForInterval(nbf, types.Uint(col.Tag), typedSet)

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
			return noms.NewNomsRangeReader(sch, m, []*noms.ReadRange{r}), nil
		}, nil

	case setalgebra.CompositeSet:
		var ranges []*noms.ReadRange
		for _, interval := range typedSet.Intervals {
			r, err := rangeForInterval(nbf, types.Uint(col.Tag), interval)

			if err != nil {
				return nil, err
			}

			ranges = append(ranges, r)
		}

		if len(typedSet.Set.HashToVal) > 0 {
			rangesForFS, err := rangesForFiniteSetOfPartialKeys(nbf, types.Uint(col.Tag), typedSet.Set)

			if err != nil {
				return nil, err
			}

			ranges = append(ranges, rangesForFS...)
		}

		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
			var readers = []table.TableReadCloser{noms.NewNomsRangeReader(sch, m, ranges)}
			return table.NewCompositeTableReader(readers)
		}, nil
	}

	return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
		return noms.NewNomsMapReader(ctx, m, sch)
	}, nil
}

func getCreateFuncForSinglePK(nbf *types.NomsBinFormat, keySet setalgebra.Set, sch schema.Schema) (CreateReaderFunc, error) {
	pkCols := sch.GetPKCols()
	col := pkCols.GetByIndex(0)
	switch typedSet := keySet.(type) {
	case setalgebra.FiniteSet:
		keys, err := finiteSetToKeySlice(nbf, types.Uint(col.Tag), typedSet)

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
			return noms.NewNomsMapReaderForKeys(m, sch, keys), nil
		}, nil

	case setalgebra.Interval:
		r, err := rangeForInterval(nbf, types.Uint(col.Tag), typedSet)

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
			return noms.NewNomsRangeReader(sch, m, []*noms.ReadRange{r}), nil
		}, nil

	case setalgebra.CompositeSet:
		var ranges []*noms.ReadRange
		for _, interval := range typedSet.Intervals {
			r, err := rangeForInterval(nbf, types.Uint(col.Tag), interval)

			if err != nil {
				return nil, err
			}

			ranges = append(ranges, r)
		}

		var keys []types.Tuple
		if len(typedSet.Set.HashToVal) > 0 {
			var err error
			keys, err = finiteSetToKeySlice(nbf, types.Uint(col.Tag), typedSet.Set)

			if err != nil {
				return nil, err
			}
		}

		return func(ctx context.Context, m types.Map) (reader table.TableReadCloser, err error) {
			var readers = []table.TableReadCloser{noms.NewNomsRangeReader(sch, m, ranges)}

			if len(keys) > 0 {
				rd := noms.NewNomsMapReaderForKeys(m, sch, keys)
				readers = append(readers, rd)
			}

			return table.NewCompositeTableReader(readers)
		}, nil
	}

	panic("unhandled case")
}

type checkEquals struct{ types.Value }

func (ce checkEquals) Check(ctx context.Context, tuple types.Tuple) (valid bool, skip bool, err error) {
	rowsFirstPKElemVal, err := tuple.Get(1)
	if err != nil {
		return false, false, err
	}
	return (ce.Value).Equals(rowsFirstPKElemVal), false, nil
}

type checkLessThan struct{ types.Value }

func (clt checkLessThan) Check(ctx context.Context, t types.Tuple) (valid bool, skip bool, err error) {
	keyVal, err := t.Get(1)
	if err != nil {
		return false, false, err
	}
	ok, err := keyVal.Less(t.Format(), clt.Value)
	return ok, false, err
}

type checkLessThanOrEquals struct{ types.Value }

func (clte checkLessThanOrEquals) Check(ctx context.Context, t types.Tuple) (valid bool, skip bool, err error) {
	keyVal, err := t.Get(1)
	if err != nil {
		return false, false, err
	}
	if keyVal.Equals(clte.Value) {
		return true, false, nil
	}
	ok, err := keyVal.Less(t.Format(), clte.Value)
	return ok, false, err
}
