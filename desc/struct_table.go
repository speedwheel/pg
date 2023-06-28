package desc

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// ConvertStructToTable takes a table name and a reflect.Type that represents a struct type
// and returns a pointer to a Table that represents a table definition for the database
// or an error if the conversion fails.
func ConvertStructToTable(tableName string, typ reflect.Type) (*Table, error) {
	if kind := typ.Kind(); kind != reflect.Struct { // check if the type is a struct
		return nil, fmt.Errorf("invalid type: expected a struct value but got: %s", kind.String()) // if not, return an error
	}

	definition := &Table{ // create a new Table with the following fields
		SearchPath: DefaultSearchPath, // use the default search path
		Name:       tableName,         // use the given table name
		StructName: typ.Name(),        // use the name of the struct type
	}

	// Retrieve only fields valid for postgres.
	pgFields := lookupFields(typ, nil)           // get the exported fields of the struct that have a non-empty and non-dash value for the 'pg' tag
	columns := make([]*Column, 0, len(pgFields)) // make a slice of pointers to Column with the same capacity as the number of fields
	for _, field := range pgFields {             // loop over the fields
		column, err := convertStructFieldToColumnDefinion(tableName, field) // convert each field to a column definition
		if err != nil {                                                     // if there is an error
			return nil, err // return the error
		}

		// set the parent table reference.
		column.Table = definition

		columns = append(columns, column) // append the column definition to the slice
	}

	// use the slice of column definitions
	definition.Columns = columns

	return definition, nil // return the table definition and no error
}

const (
	leftParenLiteral  = '('
	rightParenLiteral = ')'
	nullLiteral       = "null"

	genRandomUUIDPGCryptoFunction1 = "gen_random_uuid()"
	genRandomUUIDPGCryptoFunction2 = "uuid_generate_v4()"
)

// convertStructFieldToColumnDefinion takes a table name and a reflect.StructField that represents a struct field
// and returns a pointer to a Column that represents a column definition for the database
// or an error if the conversion fails.
func convertStructFieldToColumnDefinion(tableName string, field reflect.StructField) (*Column, error) {
	c := &Column{
		TableName:  tableName,
		Name:       SnakeCase(field.Name),
		Type:       goTypeToDataType(field.Type),
		FieldIndex: field.Index,
		FieldType:  field.Type,
		FieldName:  field.Name,
	}

	fieldTag := field.Tag.Get(DefaultTag)

	options := strings.Split(fieldTag, ",")
	for _, opt := range options {
		if opt == "" {
			continue // skip empty, e.g name,
		}

		var key, value string

		kv := strings.SplitN(opt, "=", 2)
		switch len(kv) {
		case 1: // shorthand for boolean options and default values if tag exists.
			key = kv[0]

			if opt == "index" {
				value = Btree.String()
			} else if opt == "unique_index" {
				// keep the value as it is.
			} else {
				value = "true"
			}
		case 2:
			key = kv[0]
			value = kv[1]
		default: // should never happen as "N=2".
			return c, fmt.Errorf("struct field: %s: option: %s: expected key value separated by '='", field.Name, opt)
		}

		switch key {
		case "name":
			c.Name = value
		case "type":
			if leftParenIndex := strings.IndexByte(value, leftParenLiteral); leftParenIndex > 0 {
				// contains type arguments, e.g. length of varchar.
				rightParenIndex := strings.IndexByte(value, rightParenLiteral)
				if rightParenIndex == -1 {
					return c, fmt.Errorf("struct field: %s: option: %s: type: missing right parenthesis", field.Name, opt)
				}

				c.TypeArgument = value[leftParenIndex+1 : rightParenIndex]
				value = strings.TrimSpace(value[0:leftParenIndex])
			}

			c.Type, _ = ParseDataType(value) // don't take the argument, as we need validation on this step, this syndax came from end-developer.

			// make ts_vector types unscannable by-default.
			if c.Type == TsVector {
				c.Unscannable = true
			}
		case "primary", "pk":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}
			c.PrimaryKey = v
		case "identity":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}
			c.Identity = v
			c.AutoGenerated = true
		// case "required", "not_null", "notnull":
		// 	v, err := strconv.ParseBool(value)
		// 	if err != nil {
		// 		return c, err
		// 	}
		// 	c.Required = v
		case "default": // can't contain ',' or '='.
			c.Default = value
			if value == nullLiteral {
				c.Nullable = true // set Nullable to true if the default value is "null".
			}
			//  else {
			// 	c.Required = true
			// }
		case "unique":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}

			// if c.Unique && c.UniqueIndex != "" {
			// 	return c, fmt.Errorf("unqiue and unique_index cannot be used together")
			// }

			c.Unique = v
		case "conflict":
			c.Conflict = value // note that: only one ON CONFLICT is allowed and it's set to all unique columns.
		case "username":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}
			c.Username = v
		case "password":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}
			c.Password = v
		case "nullable", "null":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}
			if v {
				c.Default = nullLiteral
				c.Nullable = v
			} else {
				if c.Default == nullLiteral { // clear default=null if nullable was forcely set to false.
					c.Default = ""
				}
			}
		case "ref", "reference", "references":
			if !strings.Contains(value, "(") {
				// If no specific tablename(column) syntax
				// then set it to this table: thisTableName(value) syntax.
				value = tableName + "(" + value + ")"
			}

			idx := strings.IndexRune(value, '(')
			if idx == -1 || len(value)+1 <= idx {
				return c, fmt.Errorf("struct field: %s: invalid reference tag: %s", field.Name, fieldTag)
			}

			refTableName := value[0:idx]
			refColumnNameLine := strings.Split(value[idx+1:len(value)-1], " ") // e.g. "ref=blogs(id cascade deferrable)"

			c.ReferenceTableName = refTableName
			c.ReferenceColumnName = refColumnNameLine[0]

			if len(refColumnNameLine) > 1 {
				c.ReferenceOnDelete = strings.ToUpper(refColumnNameLine[1])
			} else {
				c.ReferenceOnDelete = "CASCADE"
			}

			if len(refColumnNameLine) > 2 {
				c.DeferrableReference = strings.ToUpper(refColumnNameLine[2]) == "DEFERRABLE"
			}

		case "index":
			idx := parseIndexType(value)
			if idx == InvalidIndex {
				return c, fmt.Errorf("struct field: %s: invalid index type on tag: %s: value: %s", field.Name, fieldTag, value)
			}

			c.Index = idx
		case "unique_index":
			c.UniqueIndex = value
			// c.Unique = true
			if c.Unique {
				return c, fmt.Errorf("unqiue and unique_index cannot be used together")
			}
		case "check":
			c.CheckConstraint = value
		case "auto":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}

			c.AutoGenerated = v
		case "presenter":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}

			c.Presenter = v
		case "unscannable":
			v, err := strconv.ParseBool(value)
			if err != nil {
				return c, err
			}
			c.Unscannable = v
		default:
			if !strings.Contains(opt, ",") {
				// we expect this is just a name (e.g. `pg:"id"`).
				c.Name = key
			} else {
				return c, fmt.Errorf("unexpected tag option: %s", key)
			}
		}
	}

	if c.PrimaryKey && !c.Nullable && c.Type == UUID && c.Default == "" {
		c.Default = genRandomUUIDPGCryptoFunction1
	}

	if c.Password && c.Type == InvalidDataType {
		c.Type = Text
	}

	if c.Type == InvalidDataType {
		return c, fmt.Errorf("struct field: %s: invalid data type on tag: %s", field.Name, fieldTag)
	}

	return c, nil
}
