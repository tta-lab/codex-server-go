#!/usr/bin/env python3
"""Generate Go types from Codex JSON Schema.

Reads codex_app_server_protocol.schemas.json and produces:
  - protocol/types_gen.go    (structs, enums, type aliases)
  - protocol/unions_gen.go   (discriminated unions with custom UnmarshalJSON)
  - protocol/methods_gen.go  (method/notification/request constants)
"""

import json
import os
import re
import sys
import textwrap
from collections import OrderedDict
from pathlib import Path

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def to_go_name(name: str) -> str:
    """Convert schema name to Go exported identifier.

    Examples:
      ThreadStartParams -> ThreadStartParams
      v2/ThreadStartParams -> ThreadStartParams (strip v2/)
      commandExecution -> CommandExecution
      item/commandExecution/requestApproval -> ItemCommandExecutionRequestApproval
    """
    name = name.removeprefix("v2/")
    # Convert Rust-style namespace qualifiers (e.g., "ApiKeyv2::LoginAccountParams")
    # to just concatenation: "ApiKeyLoginAccountParams"
    name = name.replace("::", "")
    # Remove "v2" that appears as part of the Rust namespace prefix
    name = re.sub(r'v2(?=[A-Z])', '', name)
    # Split on / and camelCase join
    parts = name.split("/")
    result = ""
    for part in parts:
        # Capitalize first letter of each part
        if part:
            result += part[0].upper() + part[1:]
    # Handle acronyms
    result = result.replace("Jsonrpc", "JSONRPC")
    result = result.replace("Rpc", "RPC")
    result = result.replace("Url", "URL")
    result = result.replace("Id", "ID")
    result = result.replace("Uuid", "UUID")
    result = result.replace("Cpu", "CPU")
    result = result.replace("Ip", "IP")
    result = result.replace("Os", "OS")
    result = result.replace("Io", "IO")
    result = result.replace("Api", "API")
    result = result.replace("Mcp", "MCP")
    result = result.replace("Tls", "TLS")
    result = result.replace("Ssh", "SSH")
    result = result.replace("Aws", "AWS")
    # Fix over-corrections
    result = result.replace("MacOS", "MacOs")  # Keep MacOs as convention
    result = result.replace("VideOS", "Videos")
    result = result.replace("StudioS", "Studios")
    result = result.replace("SessionID", "SessionId")  # avoid double-caps on compound words
    return result


def to_go_field_name(name: str) -> str:
    """Convert JSON field name to Go exported field name."""
    # Strip leading underscores
    name = name.lstrip("_")
    if not name:
        return "Field"

    # Special cases
    if name == "id":
        return "ID"
    if name == "url":
        return "URL"
    if name == "type":
        return "Type"
    if name == "api":
        return "API"

    # Handle snake_case fields
    if "_" in name:
        parts = name.split("_")
        name = "".join(p.capitalize() for p in parts if p)
        return name

    # camelCase -> PascalCase
    result = name[0].upper() + name[1:] if name else name

    # Handle common acronyms mid-word
    result = result.replace("Id", "ID").replace("Url", "URL").replace("Api", "API")
    result = result.replace("Mcp", "MCP").replace("Cpu", "CPU").replace("Ip", "IP")
    # Fix: don't break words like "provide" -> "provIDe"
    # Only replace when it's at the end or followed by uppercase
    # Actually let's be more conservative - only capitalize well-known suffixes
    # Revert and do it more carefully
    result = name[0].upper() + name[1:] if name else name

    # Only replace ID/URL at word boundaries (end of string or before uppercase)
    result = re.sub(r'Id$', 'ID', result)
    result = re.sub(r'Id([A-Z])', r'ID\1', result)
    result = re.sub(r'Url$', 'URL', result)
    result = re.sub(r'Url([A-Z])', r'URL\1', result)
    result = re.sub(r'Api$', 'API', result)
    result = re.sub(r'Api([A-Z])', r'API\1', result)
    result = re.sub(r'Mcp$', 'MCP', result)
    result = re.sub(r'Mcp([A-Z])', r'MCP\1', result)

    return result


def json_type_to_go(schema_types, is_nullable: bool = False) -> str:
    """Convert JSON Schema type(s) to Go type."""
    if isinstance(schema_types, list):
        # ["string", "null"] pattern
        non_null = [t for t in schema_types if t != "null"]
        nullable = "null" in schema_types
        if len(non_null) == 1:
            base = _simple_type(non_null[0])
            if nullable:
                return f"*{base}"
            return base
        return "interface{}"
    return _simple_type(schema_types)


def _simple_type(t: str) -> str:
    mapping = {
        "string": "string",
        "integer": "int64",
        "number": "float64",
        "boolean": "bool",
        "object": "map[string]interface{}",
        "array": "[]interface{}",
    }
    return mapping.get(t, "interface{}")


def resolve_ref(ref: str) -> str:
    """Extract type name from $ref string like '#/definitions/v2/Foo'."""
    prefix = "#/definitions/"
    if ref.startswith(prefix):
        return ref[len(prefix):]
    return ref


def go_type_for_schema(prop, all_defs: dict) -> str:
    """Determine Go type for a property schema."""
    # Handle non-dict schemas (e.g., additionalProperties: true)
    if not isinstance(prop, dict):
        return "interface{}"

    # Direct $ref
    if "$ref" in prop:
        ref_name = resolve_ref(prop["$ref"])
        return to_go_name(ref_name)

    # anyOf with null (nullable ref)
    if "anyOf" in prop:
        variants = prop["anyOf"]
        non_null = [v for v in variants if v.get("type") != "null"]
        has_null = any(v.get("type") == "null" for v in variants)
        if len(non_null) == 1 and has_null:
            inner = go_type_for_schema(non_null[0], all_defs)
            return f"*{inner}"
        if len(non_null) == 1:
            return go_type_for_schema(non_null[0], all_defs)
        return "json.RawMessage"

    # oneOf
    if "oneOf" in prop:
        return "json.RawMessage"

    # type field
    schema_type = prop.get("type")
    if schema_type is None:
        return "json.RawMessage"

    # Array with items
    if schema_type == "array" or (isinstance(schema_type, list) and "array" in schema_type):
        items = prop.get("items", {})
        if isinstance(schema_type, list):
            has_null = "null" in schema_type
            item_type = go_type_for_schema(items, all_defs) if items else "interface{}"
            # Nullable array -> pointer to slice
            if has_null:
                return f"[]{ item_type }"  # nil slice is fine for null
            return f"[]{item_type}"
        item_type = go_type_for_schema(items, all_defs) if items else "interface{}"
        return f"[]{item_type}"

    # Nullable primitive ["string", "null"]
    if isinstance(schema_type, list):
        non_null = [t for t in schema_type if t != "null"]
        has_null = "null" in schema_type
        if len(non_null) == 1:
            # Check for object with additionalProperties
            if non_null[0] == "object":
                add_props = prop.get("additionalProperties")
                if add_props is True or add_props == {}:
                    base = "map[string]interface{}"
                elif isinstance(add_props, dict):
                    val_type = go_type_for_schema(add_props, all_defs)
                    base = f"map[string]{val_type}"
                else:
                    base = "map[string]interface{}"
                if has_null:
                    return base  # nil map is fine
                return base
            base = _simple_type(non_null[0])
            if has_null:
                return f"*{base}"
            return base
        return "interface{}"

    # Simple type
    if schema_type == "object":
        add_props = prop.get("additionalProperties")
        if "properties" not in prop:
            if add_props is True or add_props == {} or add_props is None:
                return "map[string]interface{}"
            if isinstance(add_props, dict):
                val_type = go_type_for_schema(add_props, all_defs)
                return f"map[string]{val_type}"
            return "map[string]interface{}"
        # Inline object with properties - should be a named type
        return "json.RawMessage"

    if schema_type == "string" and "enum" in prop:
        return "string"  # Will be an enum constant

    if schema_type == "string" and prop.get("format") == "int64":
        return "int64"

    return _simple_type(schema_type)


# ---------------------------------------------------------------------------
# Code generation
# ---------------------------------------------------------------------------

def _sanitize_desc(desc: str) -> str:
    """Collapse multi-line description to a single line."""
    if not desc:
        return ""
    return " ".join(desc.split())


class CodeGenerator:
    def __init__(self, schema_path: str):
        with open(schema_path) as f:
            self.schema = json.load(f)
        self.defs = self.schema["definitions"]
        self.v2 = self.defs.get("v2", {})
        # Merge all defs into a flat map with original key
        self.all_defs = {}
        for k, v in self.defs.items():
            if k == "v2":
                continue
            self.all_defs[k] = v
        for k, v in self.v2.items():
            self.all_defs[f"v2/{k}"] = v

        self.types_code: list[str] = []
        self.unions_code: list[str] = []
        self.methods_code: list[str] = []

        # Track which types are generated to avoid duplicates
        self.generated = set()
        # Track name collisions between top-level and v2
        self._check_collisions()

    def _check_collisions(self):
        """Check for name collisions between top-level and v2 defs."""
        top_names = {to_go_name(k) for k in self.defs if k != "v2"}
        v2_names = {to_go_name(f"v2/{k}") for k in self.v2}
        collisions = top_names & v2_names
        if collisions:
            # Prefix v2 collisions with V2
            self.v2_prefix_names = collisions
        else:
            self.v2_prefix_names = set()

    def go_name(self, schema_key: str) -> str:
        """Get Go name, handling v2 collisions."""
        name = to_go_name(schema_key)
        if schema_key.startswith("v2/") and name in self.v2_prefix_names:
            return "V2" + name
        return name

    def generate_all(self):
        """Generate all code."""
        self._generate_types()
        self._generate_unions()
        self._generate_methods()

    def _generate_types(self):
        """Generate structs, enums, and type aliases."""
        lines = []
        lines.append("// Code generated by cmd/codegen/main.py; DO NOT EDIT.\n")
        lines.append("package protocol\n")
        lines.append("import (\n\t\"encoding/json\"\n)\n")
        lines.append("// Ensure json import is used.\nvar _ json.RawMessage\n")

        # Sort for deterministic output
        for key in sorted(self.all_defs.keys()):
            defn = self.all_defs[key]
            go_name = self.go_name(key)

            if go_name in self.generated:
                continue

            # Skip union types (handled in unions_gen.go)
            if self._is_discriminated_union(key, defn):
                continue

            # Enum
            if "enum" in defn:
                lines.append(self._gen_enum(go_name, defn))
                self.generated.add(go_name)
                continue

            # String alias
            if defn.get("type") == "string" and "enum" not in defn:
                desc = _sanitize_desc(defn.get("description", ""))
                if desc:
                    lines.append(f"// {go_name} {desc}")
                lines.append(f"type {go_name} = string\n")
                self.generated.add(go_name)
                continue

            # anyOf (non-nullable union)
            if "anyOf" in defn and "properties" not in defn:
                lines.append(self._gen_anyof_type(go_name, key, defn))
                self.generated.add(go_name)
                continue

            # oneOf (non-method discriminated)
            if "oneOf" in defn:
                # These will be raw message types unless they're discriminated
                lines.append(self._gen_oneof_type(go_name, key, defn))
                self.generated.add(go_name)
                continue

            # Object / struct
            if defn.get("type") == "object" or "properties" in defn:
                lines.append(self._gen_struct(go_name, key, defn))
                self.generated.add(go_name)
                continue

            # Fallback
            lines.append(f"// {go_name} - unhandled schema pattern")
            lines.append(f"type {go_name} = json.RawMessage\n")
            self.generated.add(go_name)

        self.types_code = lines

    def _is_discriminated_union(self, key: str, defn: dict) -> bool:
        """Check if this is a method-discriminated union (ClientRequest, ServerNotification, etc.)."""
        if "oneOf" not in defn:
            return False
        # Check if variants have a 'method' field with enum
        variants = defn["oneOf"]
        if not variants:
            return False
        # Check first variant
        v = variants[0]
        props = v.get("properties", {})
        return "method" in props and "enum" in props.get("method", {})

    def _gen_enum(self, go_name: str, defn: dict) -> str:
        """Generate a string enum type with constants."""
        lines = []
        desc = _sanitize_desc(defn.get("description", ""))
        if desc:
            lines.append(f"// {go_name} {desc}")

        # Check if it's a string enum or integer enum
        enum_type = defn.get("type", "string")
        if enum_type == "integer" or enum_type == "number":
            lines.append(f"type {go_name} int64\n")
            lines.append("const (")
            for val in defn["enum"]:
                const_name = f"{go_name}{str(val).replace('-', 'Neg')}"
                lines.append(f"\t{const_name} {go_name} = {val}")
            lines.append(")\n")
            return "\n".join(lines)

        lines.append(f"type {go_name} string\n")
        lines.append("const (")
        for val in defn["enum"]:
            # Convert enum value to Go constant name
            const_suffix = self._enum_val_to_const(val)
            const_name = f"{go_name}{const_suffix}"
            lines.append(f'\t{const_name} {go_name} = "{val}"')
        lines.append(")\n")
        return "\n".join(lines)

    def _enum_val_to_const(self, val: str) -> str:
        """Convert enum value string to Go constant suffix."""
        # Replace hyphens and underscores with splits, then PascalCase join
        # Handle kebab-case, snake_case, and mixed
        parts = re.split(r'[-_]', val)
        if len(parts) > 1:
            return "".join(p.capitalize() for p in parts if p)
        # Handle camelCase
        if val and val[0].islower():
            return val[0].upper() + val[1:]
        # Handle SCREAMING_CASE
        if val.isupper() and "_" in val:
            parts = val.split("_")
            return "".join(p.capitalize() for p in parts)
        return val

    def _gen_anyof_type(self, go_name: str, key: str, defn: dict) -> str:
        """Generate type for anyOf patterns."""
        desc = _sanitize_desc(defn.get("description", ""))
        variants = defn["anyOf"]
        non_null = [v for v in variants if v.get("type") != "null"]
        has_null = any(v.get("type") == "null" for v in variants)

        # Simple nullable ref
        if len(non_null) == 1 and has_null:
            inner = go_type_for_schema(non_null[0], self.all_defs)
            if desc:
                return f"// {go_name} {desc}\ntype {go_name} = *{inner}\n"
            return f"type {go_name} = *{inner}\n"

        # Multiple variant union -> json.RawMessage
        if desc:
            return f"// {go_name} {desc}\ntype {go_name} = json.RawMessage\n"
        return f"type {go_name} = json.RawMessage\n"

    def _gen_oneof_type(self, go_name: str, key: str, defn: dict) -> str:
        """Generate type for oneOf patterns (non-method-discriminated)."""
        desc = _sanitize_desc(defn.get("description", ""))
        variants = defn.get("oneOf", [])

        # Check if variants use a 'type' field discriminator
        has_type_disc = all(
            "type" in v.get("properties", {}) and "enum" in v.get("properties", {}).get("type", {})
            for v in variants
            if v.get("properties")
        )

        # For now, generate as json.RawMessage with variant type constants
        lines = []
        if desc:
            lines.append(f"// {go_name} {desc}")
        lines.append(f"type {go_name} = json.RawMessage\n")

        # Generate variant structs if they have properties
        for v in variants:
            title = v.get("title", "")
            if not title or not v.get("properties"):
                continue
            variant_name = to_go_name(title)
            if variant_name not in self.generated:
                lines.append(self._gen_struct(variant_name, title, v))
                self.generated.add(variant_name)

        return "\n".join(lines)

    def _gen_struct(self, go_name: str, key: str, defn: dict) -> str:
        """Generate a Go struct."""
        lines = []
        desc = _sanitize_desc(defn.get("description", ""))
        if desc:
            single_line = " ".join(desc.split())
            lines.append(f"// {go_name} {single_line}")

        props = defn.get("properties", {})
        required = set(defn.get("required", []))

        if not props:
            lines.append(f"type {go_name} struct{{}}\n")
            return "\n".join(lines)

        lines.append(f"type {go_name} struct {{")

        for field_name in sorted(props.keys()):
            prop = props[field_name]
            if not isinstance(prop, dict):
                continue  # Skip non-dict properties (e.g., additionalProperties: true)
            go_field = to_go_field_name(field_name)
            go_type = go_type_for_schema(prop, self.all_defs)

            # If field is not required, make it a pointer (unless already pointer or slice/map)
            is_optional = field_name not in required
            if is_optional and not go_type.startswith("*") and not go_type.startswith("[]") and not go_type.startswith("map[") and go_type not in ("json.RawMessage", "interface{}"):
                go_type = f"*{go_type}"
            # Clean up double pointers
            while "**" in go_type:
                go_type = go_type.replace("**", "*")

            # JSON tag
            omitempty = ",omitempty" if is_optional else ""
            tag = f'`json:"{field_name}{omitempty}"`'

            # Field description (single line only, strip newlines)
            field_desc = prop.get("description", "")
            if field_desc:
                # Collapse multi-line descriptions to single line
                single_line = " ".join(field_desc.split())
                lines.append(f"\t// {single_line}")
            lines.append(f"\t{go_field} {go_type} {tag}")

        lines.append("}\n")
        return "\n".join(lines)

    def _generate_unions(self):
        """Generate discriminated union types with custom UnmarshalJSON."""
        lines = []
        lines.append("// Code generated by cmd/codegen/main.py; DO NOT EDIT.\n")
        lines.append("package protocol\n")
        lines.append("import (\n\t\"encoding/json\"\n\t\"fmt\"\n)\n")

        for key in sorted(self.all_defs.keys()):
            defn = self.all_defs[key]
            if not self._is_discriminated_union(key, defn):
                continue

            go_name = self.go_name(key)
            lines.append(self._gen_discriminated_union(go_name, key, defn))
            self.generated.add(go_name)

        self.unions_code = lines

    def _gen_discriminated_union(self, go_name: str, key: str, defn: dict) -> str:
        """Generate a discriminated union with Method field and Params/Result as json.RawMessage."""
        lines = []
        desc = _sanitize_desc(defn.get("description", ""))
        if desc:
            lines.append(f"// {go_name} {desc}")

        variants = defn["oneOf"]

        # Determine field structure from first variant
        sample_props = variants[0].get("properties", {})
        has_id = "id" in sample_props
        has_params = "params" in sample_props
        has_result = "result" in sample_props
        has_error = "error" in sample_props

        lines.append(f"type {go_name} struct {{")
        if has_id:
            lines.append(f'\tID RequestID `json:"id"`')
        lines.append(f'\tMethod string `json:"method"`')
        if has_params:
            lines.append(f'\tParams json.RawMessage `json:"params,omitempty"`')
        if has_result:
            lines.append(f'\tResult json.RawMessage `json:"result,omitempty"`')
        if has_error:
            lines.append(f'\tError json.RawMessage `json:"error,omitempty"`')
        lines.append("}\n")

        # Generate typed params getter methods
        for v in variants:
            title = v.get("title", "")
            props = v.get("properties", {})
            method_val = props.get("method", {}).get("enum", [""])[0]
            if not method_val:
                continue

            # Generate params type references
            if has_params and "params" in props:
                params_ref = props["params"]
                params_type = go_type_for_schema(params_ref, self.all_defs)
                if params_type and params_type not in ("json.RawMessage", "interface{}"):
                    method_name = self._method_to_func_name(method_val)
                    lines.append(f"// {method_name}Params unmarshals params for method \"{method_val}\".")
                    lines.append(f"func (m *{go_name}) {method_name}Params() (*{params_type}, error) {{")
                    lines.append(f"\tvar v {params_type}")
                    lines.append(f"\tif err := json.Unmarshal(m.Params, &v); err != nil {{")
                    lines.append(f'\t\treturn nil, fmt.Errorf("unmarshal {method_val} params: %w", err)')
                    lines.append(f"\t}}")
                    lines.append(f"\treturn &v, nil")
                    lines.append(f"}}\n")

        return "\n".join(lines)

    def _method_to_func_name(self, method: str) -> str:
        """Convert method string like 'thread/start' to 'ThreadStart'."""
        parts = method.split("/")
        return "".join(p[0].upper() + p[1:] for p in parts if p)

    def _generate_methods(self):
        """Generate method constants from discriminated unions."""
        lines = []
        lines.append("// Code generated by cmd/codegen/main.py; DO NOT EDIT.\n")
        lines.append("package protocol\n")

        # ClientRequest methods
        cr = self.all_defs.get("ClientRequest", {})
        if cr:
            lines.append("// Client request methods.")
            lines.append("const (")
            for v in cr.get("oneOf", []):
                props = v.get("properties", {})
                method_val = props.get("method", {}).get("enum", [""])[0]
                if method_val:
                    const_name = "Method" + self._method_to_func_name(method_val)
                    lines.append(f'\t{const_name} = "{method_val}"')
            lines.append(")\n")

        # ServerNotification methods
        sn = self.all_defs.get("ServerNotification", {})
        if sn:
            lines.append("// Server notification methods.")
            lines.append("const (")
            for v in sn.get("oneOf", []):
                props = v.get("properties", {})
                method_val = props.get("method", {}).get("enum", [""])[0]
                if method_val:
                    const_name = "Notif" + self._method_to_func_name(method_val)
                    lines.append(f'\t{const_name} = "{method_val}"')
            lines.append(")\n")

        # ServerRequest methods
        sr = self.all_defs.get("ServerRequest", {})
        if sr:
            lines.append("// Server request methods.")
            lines.append("const (")
            for v in sr.get("oneOf", []):
                props = v.get("properties", {})
                method_val = props.get("method", {}).get("enum", [""])[0]
                if method_val:
                    const_name = "Req" + self._method_to_func_name(method_val)
                    lines.append(f'\t{const_name} = "{method_val}"')
            lines.append(")\n")

        # ClientNotification methods
        cn = self.all_defs.get("ClientNotification", {})
        if cn and "oneOf" in cn:
            lines.append("// Client notification methods.")
            lines.append("const (")
            for v in cn.get("oneOf", []):
                props = v.get("properties", {})
                method_val = props.get("method", {}).get("enum", [""])[0]
                if method_val:
                    const_name = "ClientNotif" + self._method_to_func_name(method_val)
                    lines.append(f'\t{const_name} = "{method_val}"')
            lines.append(")\n")

        # EventMsg methods (if they have method fields)
        em = self.all_defs.get("EventMsg", {})
        if em and "oneOf" in em:
            has_method = False
            for v in em.get("oneOf", []):
                props = v.get("properties", {})
                if "method" in props and "enum" in props.get("method", {}):
                    has_method = True
                    break
            if has_method:
                lines.append("// Event message methods.")
                lines.append("const (")
                for v in em.get("oneOf", []):
                    props = v.get("properties", {})
                    method_val = props.get("method", {}).get("enum", [""])[0]
                    if method_val:
                        const_name = "Event" + self._method_to_func_name(method_val)
                        lines.append(f'\t{const_name} = "{method_val}"')
                lines.append(")\n")

        self.methods_code = lines

    def write_files(self, output_dir: str):
        """Write generated files."""
        os.makedirs(output_dir, exist_ok=True)

        with open(os.path.join(output_dir, "types_gen.go"), "w") as f:
            f.write("\n".join(self.types_code))

        with open(os.path.join(output_dir, "unions_gen.go"), "w") as f:
            f.write("\n".join(self.unions_code))

        with open(os.path.join(output_dir, "methods_gen.go"), "w") as f:
            f.write("\n".join(self.methods_code))


def main():
    repo_root = Path(__file__).parent.parent.parent
    schema_path = repo_root / "schema" / "codex_app_server_protocol.schemas.json"
    output_dir = repo_root / "protocol"

    if not schema_path.exists():
        print(f"Schema not found: {schema_path}", file=sys.stderr)
        sys.exit(1)

    gen = CodeGenerator(str(schema_path))
    gen.generate_all()
    gen.write_files(str(output_dir))

    # Run gofmt
    import subprocess
    result = subprocess.run(["gofmt", "-w", str(output_dir)], capture_output=True, text=True)
    if result.returncode != 0:
        print(f"gofmt failed: {result.stderr}", file=sys.stderr)
        sys.exit(1)

    # Count types
    print(f"Generated types in {output_dir}/")
    for fname in ["types_gen.go", "unions_gen.go", "methods_gen.go"]:
        fpath = output_dir / fname
        if fpath.exists():
            line_count = sum(1 for _ in open(fpath))
            print(f"  {fname}: {line_count} lines")


if __name__ == "__main__":
    main()
