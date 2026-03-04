// Command codegen generates Go types from Codex JSON Schema.
//
// It reads codex_app_server_protocol.schemas.json and produces:
//   - protocol/types_gen.go    (structs, enums, type aliases)
//   - protocol/unions_gen.go   (discriminated unions with custom UnmarshalJSON)
//   - protocol/methods_gen.go  (method/notification/request constants)
package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Compiled regexes
// ---------------------------------------------------------------------------

var (
	reV2Upper    = regexp.MustCompile(`v2([A-Z])`)
	reIDEnd      = regexp.MustCompile(`Id$`)
	reIDUpper    = regexp.MustCompile(`Id([A-Z])`)
	reURLEnd     = regexp.MustCompile(`Url$`)
	reURLUpper   = regexp.MustCompile(`Url([A-Z])`)
	reAPIEnd     = regexp.MustCompile(`Api$`)
	reAPIUpper   = regexp.MustCompile(`Api([A-Z])`)
	reMCPEnd     = regexp.MustCompile(`Mcp$`)
	reMCPUpper   = regexp.MustCompile(`Mcp([A-Z])`)
	reSplitParts = regexp.MustCompile(`[-_]`)
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// toGoName converts a schema name to a Go exported identifier.
func toGoName(name string) string {
	name = strings.TrimPrefix(name, "v2/")
	// Convert Rust-style namespace qualifiers
	name = strings.ReplaceAll(name, "::", "")
	// Remove "v2" that appears as part of the Rust namespace prefix
	name = reV2Upper.ReplaceAllString(name, "$1")

	// Split on / and PascalCase join
	parts := strings.Split(name, "/")
	var result strings.Builder
	for _, part := range parts {
		if part != "" {
			result.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}

	r := result.String()
	// Handle acronyms
	r = strings.ReplaceAll(r, "Jsonrpc", "JSONRPC")
	r = strings.ReplaceAll(r, "Rpc", "RPC")
	r = strings.ReplaceAll(r, "Url", "URL")
	r = strings.ReplaceAll(r, "Id", "ID")
	r = strings.ReplaceAll(r, "Uuid", "UUID")
	r = strings.ReplaceAll(r, "Cpu", "CPU")
	r = strings.ReplaceAll(r, "Ip", "IP")
	r = strings.ReplaceAll(r, "Os", "OS")
	r = strings.ReplaceAll(r, "Io", "IO")
	r = strings.ReplaceAll(r, "Api", "API")
	r = strings.ReplaceAll(r, "Mcp", "MCP")
	r = strings.ReplaceAll(r, "Tls", "TLS")
	r = strings.ReplaceAll(r, "Ssh", "SSH")
	r = strings.ReplaceAll(r, "Aws", "AWS")
	// Fix over-corrections
	r = strings.ReplaceAll(r, "MacOS", "MacOs")
	r = strings.ReplaceAll(r, "VideOS", "Videos")
	r = strings.ReplaceAll(r, "StudioS", "Studios")
	r = strings.ReplaceAll(r, "SessionID", "SessionId")
	return r
}

// toGoFieldName converts a JSON field name to a Go exported field name.
func toGoFieldName(name string) string {
	name = strings.TrimLeft(name, "_")
	if name == "" {
		return "Field"
	}

	switch name {
	case "id":
		return "ID"
	case "url":
		return "URL"
	case "type":
		return "Type"
	case "api":
		return "API"
	}

	// Handle snake_case fields
	if strings.Contains(name, "_") {
		parts := strings.Split(name, "_")
		var b strings.Builder
		for _, p := range parts {
			if p != "" {
				b.WriteString(strings.ToUpper(p[:1]) + p[1:])
			}
		}
		name = b.String()
	} else {
		// camelCase -> PascalCase
		name = strings.ToUpper(name[:1]) + name[1:]
	}

	// Apply acronym fixups at word boundaries (end of string or before uppercase)
	name = reIDEnd.ReplaceAllString(name, "ID")
	name = reIDUpper.ReplaceAllString(name, "ID$1")
	name = reURLEnd.ReplaceAllString(name, "URL")
	name = reURLUpper.ReplaceAllString(name, "URL$1")
	name = reAPIEnd.ReplaceAllString(name, "API")
	name = reAPIUpper.ReplaceAllString(name, "API$1")
	name = reMCPEnd.ReplaceAllString(name, "MCP")
	name = reMCPUpper.ReplaceAllString(name, "MCP$1")

	return name
}

func simpleType(t string) string {
	switch t {
	case "string":
		return "string"
	case "integer":
		return "int64"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "object":
		return "map[string]interface{}"
	case "array":
		return "[]interface{}"
	default:
		return "interface{}"
	}
}

func resolveRef(ref string) string {
	const prefix = "#/definitions/"
	if strings.HasPrefix(ref, prefix) {
		return ref[len(prefix):]
	}
	return ref
}

// goTypeForSchema determines the Go type for a property schema.
func goTypeForSchema(prop map[string]interface{}, allDefs map[string]interface{}) string {
	if prop == nil {
		return "interface{}"
	}

	// Direct $ref
	if ref, ok := prop["$ref"].(string); ok {
		return toGoName(resolveRef(ref))
	}

	// anyOf with null (nullable ref)
	if anyOf, ok := prop["anyOf"].([]interface{}); ok {
		var nonNull []map[string]interface{}
		hasNull := false
		for _, v := range anyOf {
			vm, ok := v.(map[string]interface{})
			if !ok {
				continue
			}
			if vm["type"] == "null" {
				hasNull = true
			} else {
				nonNull = append(nonNull, vm)
			}
		}
		if len(nonNull) == 1 && hasNull {
			inner := goTypeForSchema(nonNull[0], allDefs)
			return "*" + inner
		}
		if len(nonNull) == 1 {
			return goTypeForSchema(nonNull[0], allDefs)
		}
		return "json.RawMessage"
	}

	// oneOf
	if _, ok := prop["oneOf"]; ok {
		return "json.RawMessage"
	}

	// type field
	schemaType, hasType := prop["type"]
	if !hasType {
		return "json.RawMessage"
	}

	// Type can be a string or array of strings
	switch st := schemaType.(type) {
	case string:
		return goTypeForSingleType(st, prop, allDefs)
	case []interface{}:
		return goTypeForTypeArray(st, prop, allDefs)
	}

	return "json.RawMessage"
}

func goTypeForSingleType(st string, prop map[string]interface{}, allDefs map[string]interface{}) string {
	if st == "array" {
		items, _ := prop["items"].(map[string]interface{})
		itemType := "interface{}"
		if items != nil {
			itemType = goTypeForSchema(items, allDefs)
		}
		return "[]" + itemType
	}

	if st == "object" {
		addProps := prop["additionalProperties"]
		if _, hasProp := prop["properties"]; !hasProp {
			if addProps == true || addProps == nil {
				return "map[string]interface{}"
			}
			if m, ok := addProps.(map[string]interface{}); ok {
				if len(m) == 0 {
					return "map[string]interface{}"
				}
				valType := goTypeForSchema(m, allDefs)
				return "map[string]" + valType
			}
			return "map[string]interface{}"
		}
		return "json.RawMessage"
	}

	if st == "string" {
		if _, ok := prop["enum"]; ok {
			return "string"
		}
		if prop["format"] == "int64" {
			return "int64"
		}
	}

	return simpleType(st)
}

func goTypeForTypeArray(types []interface{}, prop map[string]interface{}, allDefs map[string]interface{}) string {
	var nonNull []string
	hasNull := false
	for _, t := range types {
		ts, ok := t.(string)
		if !ok {
			continue
		}
		if ts == "null" {
			hasNull = true
		} else {
			nonNull = append(nonNull, ts)
		}
	}

	if len(nonNull) == 1 {
		// Check for array type
		if nonNull[0] == "array" {
			items, _ := prop["items"].(map[string]interface{})
			itemType := "interface{}"
			if items != nil {
				itemType = goTypeForSchema(items, allDefs)
			}
			return "[]" + itemType
		}

		// Check for object with additionalProperties
		if nonNull[0] == "object" {
			addProps := prop["additionalProperties"]
			if addProps == true {
				return "map[string]interface{}"
			}
			if m, ok := addProps.(map[string]interface{}); ok {
				if len(m) == 0 {
					return "map[string]interface{}"
				}
				valType := goTypeForSchema(m, allDefs)
				return "map[string]" + valType
			}
			return "map[string]interface{}"
		}

		base := simpleType(nonNull[0])
		if hasNull {
			return "*" + base
		}
		return base
	}

	return "interface{}"
}

// ---------------------------------------------------------------------------
// Code generator
// ---------------------------------------------------------------------------

type codeGenerator struct {
	allDefs       map[string]interface{}
	generated     map[string]bool
	v2PrefixNames map[string]bool
	typesCode     []string
	unionsCode    []string
	methodsCode   []string
}

func newCodeGenerator(schemaPath string) (*codeGenerator, error) {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}

	defs, _ := schema["definitions"].(map[string]interface{})
	if defs == nil {
		return nil, fmt.Errorf("no definitions in schema")
	}

	v2Defs, _ := defs["v2"].(map[string]interface{})

	allDefs := make(map[string]interface{})
	for k, v := range defs {
		if k == "v2" {
			continue
		}
		allDefs[k] = v
	}
	for k, v := range v2Defs {
		allDefs["v2/"+k] = v
	}

	// Check for name collisions between top-level and v2 defs
	topNames := make(map[string]bool)
	for k := range defs {
		if k == "v2" {
			continue
		}
		topNames[toGoName(k)] = true
	}
	v2PrefixNames := make(map[string]bool)
	for k := range v2Defs {
		name := toGoName("v2/" + k)
		if topNames[name] {
			v2PrefixNames[name] = true
		}
	}

	return &codeGenerator{
		allDefs:       allDefs,
		generated:     make(map[string]bool),
		v2PrefixNames: v2PrefixNames,
	}, nil
}

func (g *codeGenerator) goName(schemaKey string) string {
	name := toGoName(schemaKey)
	if strings.HasPrefix(schemaKey, "v2/") && g.v2PrefixNames[name] {
		return "V2" + name
	}
	return name
}

func (g *codeGenerator) sortedKeys() []string {
	keys := make([]string, 0, len(g.allDefs))
	for k := range g.allDefs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sanitizeDesc(desc string) string {
	if desc == "" {
		return ""
	}
	return strings.Join(strings.Fields(desc), " ")
}

func (g *codeGenerator) generateAll() {
	g.generateTypes()
	g.generateUnions()
	g.generateMethods()
}

// ---------------------------------------------------------------------------
// Types generation
// ---------------------------------------------------------------------------

func (g *codeGenerator) generateTypes() {
	var lines []string
	lines = append(lines, "// Code generated by cmd/codegen; DO NOT EDIT.\n")
	lines = append(lines, "package protocol\n")
	lines = append(lines, "import (\n\t\"encoding/json\"\n)\n")

	for _, key := range g.sortedKeys() {
		defn, _ := g.allDefs[key].(map[string]interface{})
		if defn == nil {
			continue
		}
		goName := g.goName(key)
		if g.generated[goName] {
			continue
		}

		// Skip union types (handled in unions_gen.go)
		if g.isDiscriminatedUnion(defn) {
			continue
		}

		// Enum
		if _, ok := defn["enum"]; ok {
			lines = append(lines, g.genEnum(goName, defn))
			g.generated[goName] = true
			continue
		}

		// String alias
		if defn["type"] == "string" {
			if _, ok := defn["enum"]; !ok {
				desc := sanitizeDesc(getString(defn, "description"))
				if desc != "" {
					lines = append(lines, fmt.Sprintf("// %s %s", goName, desc))
				}
				lines = append(lines, fmt.Sprintf("type %s = string\n", goName))
				g.generated[goName] = true
				continue
			}
		}

		// anyOf (non-nullable union)
		if _, ok := defn["anyOf"]; ok {
			if _, hasProp := defn["properties"]; !hasProp {
				lines = append(lines, g.genAnyOfType(goName, defn))
				g.generated[goName] = true
				continue
			}
		}

		// oneOf (non-method discriminated)
		if _, ok := defn["oneOf"]; ok {
			lines = append(lines, g.genOneOfType(goName, defn))
			g.generated[goName] = true
			continue
		}

		// Object / struct
		if defn["type"] == "object" || defn["properties"] != nil {
			lines = append(lines, g.genStruct(goName, defn))
			g.generated[goName] = true
			continue
		}

		// Fallback
		lines = append(lines, fmt.Sprintf("// %s - unhandled schema pattern", goName))
		lines = append(lines, fmt.Sprintf("type %s = json.RawMessage\n", goName))
		g.generated[goName] = true
	}

	g.typesCode = lines
}

func (g *codeGenerator) isDiscriminatedUnion(defn map[string]interface{}) bool {
	oneOf, ok := defn["oneOf"].([]interface{})
	if !ok || len(oneOf) == 0 {
		return false
	}
	first, _ := oneOf[0].(map[string]interface{})
	if first == nil {
		return false
	}
	props, _ := first["properties"].(map[string]interface{})
	if props == nil {
		return false
	}
	methodProp, _ := props["method"].(map[string]interface{})
	if methodProp == nil {
		return false
	}
	_, hasEnum := methodProp["enum"]
	return hasEnum
}

func (g *codeGenerator) genEnum(goName string, defn map[string]interface{}) string {
	var lines []string
	desc := sanitizeDesc(getString(defn, "description"))
	if desc != "" {
		lines = append(lines, fmt.Sprintf("// %s %s", goName, desc))
	}

	enumVals, _ := defn["enum"].([]interface{})
	enumType := getString(defn, "type")

	if enumType == "integer" || enumType == "number" {
		lines = append(lines, fmt.Sprintf("type %s int64\n", goName))
		lines = append(lines, "const (")
		for _, val := range enumVals {
			num := fmt.Sprintf("%v", val)
			constName := fmt.Sprintf("%s%s", goName, strings.ReplaceAll(num, "-", "Neg"))
			lines = append(lines, fmt.Sprintf("\t%s %s = %s", constName, goName, num))
		}
		lines = append(lines, ")\n")
		return strings.Join(lines, "\n")
	}

	lines = append(lines, fmt.Sprintf("type %s string\n", goName))
	lines = append(lines, "const (")
	for _, val := range enumVals {
		valStr := fmt.Sprintf("%v", val)
		constSuffix := enumValToConst(valStr)
		constName := goName + constSuffix
		lines = append(lines, fmt.Sprintf("\t%s %s = %q", constName, goName, valStr))
	}
	lines = append(lines, ")\n")
	return strings.Join(lines, "\n")
}

func enumValToConst(val string) string {
	// Replace hyphens and underscores with splits, then PascalCase join
	parts := reSplitParts.Split(val, -1)
	if len(parts) > 1 {
		var b strings.Builder
		for _, p := range parts {
			if p != "" {
				// Python's str.capitalize(): upper first, lower rest
				b.WriteString(strings.ToUpper(p[:1]) + strings.ToLower(p[1:]))
			}
		}
		return b.String()
	}

	// Handle camelCase
	if val != "" && val[0] >= 'a' && val[0] <= 'z' {
		return strings.ToUpper(val[:1]) + val[1:]
	}

	// Handle SCREAMING_CASE
	if val == strings.ToUpper(val) && strings.Contains(val, "_") {
		parts := strings.Split(val, "_")
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(strings.ToUpper(p[:1]) + strings.ToLower(p[1:]))
		}
		return b.String()
	}

	return val
}

func (g *codeGenerator) genAnyOfType(goName string, defn map[string]interface{}) string {
	desc := sanitizeDesc(getString(defn, "description"))
	variants, _ := defn["anyOf"].([]interface{})

	var nonNull []map[string]interface{}
	hasNull := false
	for _, v := range variants {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if vm["type"] == "null" {
			hasNull = true
		} else {
			nonNull = append(nonNull, vm)
		}
	}

	// Simple nullable ref
	if len(nonNull) == 1 && hasNull {
		inner := goTypeForSchema(nonNull[0], g.allDefs)
		if desc != "" {
			return fmt.Sprintf("// %s %s\ntype %s = *%s\n", goName, desc, goName, inner)
		}
		return fmt.Sprintf("type %s = *%s\n", goName, inner)
	}

	// Multiple variant union -> json.RawMessage
	if desc != "" {
		return fmt.Sprintf("// %s %s\ntype %s = json.RawMessage\n", goName, desc, goName)
	}
	return fmt.Sprintf("type %s = json.RawMessage\n", goName)
}

func (g *codeGenerator) genOneOfType(goName string, defn map[string]interface{}) string {
	desc := sanitizeDesc(getString(defn, "description"))
	variants, _ := defn["oneOf"].([]interface{})

	var lines []string
	if desc != "" {
		lines = append(lines, fmt.Sprintf("// %s %s", goName, desc))
	}
	lines = append(lines, fmt.Sprintf("type %s = json.RawMessage\n", goName))

	// Generate variant structs if they have properties
	for _, v := range variants {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		title := getString(vm, "title")
		if title == "" {
			continue
		}
		if vm["properties"] == nil {
			continue
		}
		variantName := toGoName(title)
		if !g.generated[variantName] {
			lines = append(lines, g.genStruct(variantName, vm))
			g.generated[variantName] = true
		}
	}

	return strings.Join(lines, "\n")
}

func (g *codeGenerator) genStruct(goName string, defn map[string]interface{}) string {
	var lines []string
	desc := sanitizeDesc(getString(defn, "description"))
	if desc != "" {
		lines = append(lines, fmt.Sprintf("// %s %s", goName, desc))
	}

	props, _ := defn["properties"].(map[string]interface{})
	requiredList, _ := defn["required"].([]interface{})
	required := make(map[string]bool)
	for _, r := range requiredList {
		if s, ok := r.(string); ok {
			required[s] = true
		}
	}

	if len(props) == 0 {
		lines = append(lines, fmt.Sprintf("type %s struct{}\n", goName))
		return strings.Join(lines, "\n")
	}

	lines = append(lines, fmt.Sprintf("type %s struct {", goName))

	// Sort field names for deterministic output
	fieldNames := make([]string, 0, len(props))
	for k := range props {
		fieldNames = append(fieldNames, k)
	}
	sort.Strings(fieldNames)

	for _, fieldName := range fieldNames {
		propRaw := props[fieldName]
		prop, ok := propRaw.(map[string]interface{})
		if !ok {
			continue
		}
		goField := toGoFieldName(fieldName)
		goType := goTypeForSchema(prop, g.allDefs)

		// If field is not required, make it a pointer (unless already pointer or slice/map)
		isOptional := !required[fieldName]
		if isOptional &&
			!strings.HasPrefix(goType, "*") &&
			!strings.HasPrefix(goType, "[]") &&
			!strings.HasPrefix(goType, "map[") &&
			goType != "json.RawMessage" &&
			goType != "interface{}" {
			goType = "*" + goType
		}
		// Clean up double pointers
		for strings.Contains(goType, "**") {
			goType = strings.ReplaceAll(goType, "**", "*")
		}

		// JSON tag
		omitempty := ""
		if isOptional {
			omitempty = ",omitempty"
		}
		tag := fmt.Sprintf("`json:\"%s%s\"`", fieldName, omitempty)

		// Field description
		fieldDesc := getString(prop, "description")
		if fieldDesc != "" {
			singleLine := strings.Join(strings.Fields(fieldDesc), " ")
			lines = append(lines, fmt.Sprintf("\t// %s", singleLine))
		}
		lines = append(lines, fmt.Sprintf("\t%s %s %s", goField, goType, tag))
	}

	lines = append(lines, "}\n")
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Unions generation
// ---------------------------------------------------------------------------

func (g *codeGenerator) generateUnions() {
	var lines []string
	lines = append(lines, "// Code generated by cmd/codegen; DO NOT EDIT.\n")
	lines = append(lines, "package protocol\n")
	lines = append(lines, "import (\n\t\"encoding/json\"\n\t\"fmt\"\n)\n")

	for _, key := range g.sortedKeys() {
		defn, _ := g.allDefs[key].(map[string]interface{})
		if defn == nil {
			continue
		}
		if !g.isDiscriminatedUnion(defn) {
			continue
		}
		goName := g.goName(key)
		lines = append(lines, g.genDiscriminatedUnion(goName, defn))
		g.generated[goName] = true
	}

	g.unionsCode = lines
}

func (g *codeGenerator) genDiscriminatedUnion(goName string, defn map[string]interface{}) string {
	var lines []string
	desc := sanitizeDesc(getString(defn, "description"))
	if desc != "" {
		lines = append(lines, fmt.Sprintf("// %s %s", goName, desc))
	}

	variants, _ := defn["oneOf"].([]interface{})

	// Determine field structure from first variant
	first, _ := variants[0].(map[string]interface{})
	sampleProps, _ := first["properties"].(map[string]interface{})
	_, hasID := sampleProps["id"]
	_, hasParams := sampleProps["params"]
	_, hasResult := sampleProps["result"]
	_, hasError := sampleProps["error"]

	lines = append(lines, fmt.Sprintf("type %s struct {", goName))
	if hasID {
		lines = append(lines, "\tID RequestID `json:\"id\"`")
	}
	lines = append(lines, "\tMethod string `json:\"method\"`")
	if hasParams {
		lines = append(lines, "\tParams json.RawMessage `json:\"params,omitempty\"`")
	}
	if hasResult {
		lines = append(lines, "\tResult json.RawMessage `json:\"result,omitempty\"`")
	}
	if hasError {
		lines = append(lines, "\tError json.RawMessage `json:\"error,omitempty\"`")
	}
	lines = append(lines, "}\n")

	// Generate typed params getter methods
	for _, v := range variants {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		props, _ := vm["properties"].(map[string]interface{})
		if props == nil {
			continue
		}
		methodProp, _ := props["method"].(map[string]interface{})
		if methodProp == nil {
			continue
		}
		methodEnums, _ := methodProp["enum"].([]interface{})
		if len(methodEnums) == 0 {
			continue
		}
		methodVal := fmt.Sprintf("%v", methodEnums[0])
		if methodVal == "" {
			continue
		}

		if hasParams {
			paramsProp, ok := props["params"].(map[string]interface{})
			if !ok {
				continue
			}
			paramsType := goTypeForSchema(paramsProp, g.allDefs)
			if paramsType != "" && paramsType != "json.RawMessage" && paramsType != "interface{}" {
				funcName := methodToFuncName(methodVal)
				lines = append(lines, fmt.Sprintf("// %sParams unmarshals params for method %q.", funcName, methodVal))
				lines = append(lines, fmt.Sprintf("func (m *%s) %sParams() (*%s, error) {", goName, funcName, paramsType))
				lines = append(lines, fmt.Sprintf("\tvar v %s", paramsType))
				lines = append(lines, "\tif err := json.Unmarshal(m.Params, &v); err != nil {")
				lines = append(lines, fmt.Sprintf("\t\treturn nil, fmt.Errorf(\"unmarshal %s params: %%w\", err)", methodVal))
				lines = append(lines, "\t}")
				lines = append(lines, "\treturn &v, nil")
				lines = append(lines, "}\n")
			}
		}
	}

	return strings.Join(lines, "\n")
}

func methodToFuncName(method string) string {
	parts := strings.Split(method, "/")
	var b strings.Builder
	for _, p := range parts {
		if p != "" {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Methods generation
// ---------------------------------------------------------------------------

func (g *codeGenerator) generateMethods() {
	var lines []string
	lines = append(lines, "// Code generated by cmd/codegen; DO NOT EDIT.\n")
	lines = append(lines, "package protocol\n")

	type methodGroup struct {
		key     string
		comment string
		prefix  string
	}

	groups := []methodGroup{
		{"ClientRequest", "Client request methods.", "Method"},
		{"ServerNotification", "Server notification methods.", "Notif"},
		{"ServerRequest", "Server request methods.", "Req"},
		{"ClientNotification", "Client notification methods.", "ClientNotif"},
		{"EventMsg", "Event message methods.", "Event"},
	}

	for _, grp := range groups {
		defn, _ := g.allDefs[grp.key].(map[string]interface{})
		if defn == nil {
			continue
		}
		oneOf, _ := defn["oneOf"].([]interface{})
		if len(oneOf) == 0 {
			continue
		}

		// For EventMsg, check if variants actually have method fields
		if grp.key == "EventMsg" {
			hasMethod := false
			for _, v := range oneOf {
				vm, _ := v.(map[string]interface{})
				if vm == nil {
					continue
				}
				props, _ := vm["properties"].(map[string]interface{})
				if props == nil {
					continue
				}
				methodProp, _ := props["method"].(map[string]interface{})
				if methodProp == nil {
					continue
				}
				if _, ok := methodProp["enum"]; ok {
					hasMethod = true
					break
				}
			}
			if !hasMethod {
				continue
			}
		}

		lines = append(lines, fmt.Sprintf("// %s", grp.comment))
		lines = append(lines, "const (")
		for _, v := range oneOf {
			vm, _ := v.(map[string]interface{})
			if vm == nil {
				continue
			}
			props, _ := vm["properties"].(map[string]interface{})
			if props == nil {
				continue
			}
			methodProp, _ := props["method"].(map[string]interface{})
			if methodProp == nil {
				continue
			}
			methodEnums, _ := methodProp["enum"].([]interface{})
			if len(methodEnums) == 0 {
				continue
			}
			methodVal := fmt.Sprintf("%v", methodEnums[0])
			if methodVal == "" {
				continue
			}
			constName := grp.prefix + methodToFuncName(methodVal)
			lines = append(lines, fmt.Sprintf("\t%s = %q", constName, methodVal))
		}
		lines = append(lines, ")\n")
	}

	g.methodsCode = lines
}

// ---------------------------------------------------------------------------
// File writing
// ---------------------------------------------------------------------------

func (g *codeGenerator) writeFiles(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	files := []struct {
		name  string
		lines []string
	}{
		{"types_gen.go", g.typesCode},
		{"unions_gen.go", g.unionsCode},
		{"methods_gen.go", g.methodsCode},
	}

	for _, f := range files {
		content := strings.Join(f.lines, "\n")
		// Format with go/format
		formatted, err := format.Source([]byte(content))
		if err != nil {
			return fmt.Errorf("format %s: %w\n\nRaw content:\n%s", f.name, err, content)
		}
		path := filepath.Join(outputDir, f.name)
		if err := os.WriteFile(path, formatted, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	schemaPath := filepath.Join(repoRoot, "schema", "codex_app_server_protocol.schemas.json")
	outputDir := filepath.Join(repoRoot, "protocol")

	gen, err := newCodeGenerator(schemaPath)
	if err != nil {
		log.Fatalf("Failed to initialize codegen: %v", err)
	}

	gen.generateAll()

	if err := gen.writeFiles(outputDir); err != nil {
		log.Fatalf("Failed to write files: %v", err)
	}

	// Count lines
	fmt.Printf("Generated types in %s/\n", outputDir)
	for _, fname := range []string{"types_gen.go", "unions_gen.go", "methods_gen.go"} {
		fpath := filepath.Join(outputDir, fname)
		data, err := os.ReadFile(fpath)
		if err != nil {
			continue
		}
		lineCount := strings.Count(string(data), "\n")
		fmt.Printf("  %s: %d lines\n", fname, lineCount)
	}
}
