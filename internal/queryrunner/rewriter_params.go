package queryrunner

import (
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// selectTreeContainsParamRef reports whether a positional parameter ($1, $2,
// ...) appears anywhere in the select tree: predicates, the target list,
// joins, LIMIT/OFFSET, function arguments, CTEs and subqueries. It walks every
// node of the parse tree rather than a list of clause types, so a clause added
// to the walk later cannot be missed.
func selectTreeContainsParamRef(sel *pg_query.SelectStmt) bool {
	return sel != nil && messageContainsParamRef(sel.ProtoReflect())
}

func messageContainsParamRef(m protoreflect.Message) bool {
	if m.Descriptor().FullName() == "pg_query.ParamRef" {
		return true
	}
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.Message() == nil || fd.IsMap():
		case fd.IsList():
			for i, list := 0, v.List(); i < list.Len() && !found; i++ {
				found = messageContainsParamRef(list.Get(i).Message())
			}
		default:
			found = messageContainsParamRef(v.Message())
		}
		return !found
	})
	return found
}
