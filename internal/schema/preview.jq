
def tr: if .kind == "NON_NULL" then "\(.ofType | tr)!" elif .kind == "LIST" then "[\(.ofType | tr)]" else .name end;
def base: if .ofType then .ofType | base else .name end;
def sig: "\(.name)\(if (.args | length) > 0 then "(\(.args | map("\(.name): \(.type | tr)") | join(", ")))" else "" end): \(.type | tr)";
def render: {name, kind, fields: [(.fields // [])[] | sig], inputFields: [(.inputFields // [])[] | "\(.name): \(.type | tr)"], enumValues: [(.enumValues // [])[].name]};
def body: ((.fields // [])[] | "  \(sig)"), ((.inputFields // [])[] | "  \(.name): \(.type | tr)"), ((.enumValues // [])[] | "  \(.name)");
def short: ({OBJECT: "type", INPUT_OBJECT: "input", ENUM: "enum", INTERFACE: "interface", UNION: "union"}[.] // .);

.queryType.name as $q | .mutationType.name as $m
| (.types | map({key: .name, value: .}) | from_entries) as $T
| if $kind == "query" or $kind == "mutation" then
    (if $kind == "query" then $q else $m end) as $root
    | ($T[$root].fields[] | select(.name == $n)) as $f
    | ($f | sig), "",
      (($f.type | base) as $rt | "returns \($rt)", ($T[$rt] // empty | body)),
      (($f.args // [])[] | (.type | base) as $at
        | select((($T[$at] // {}).kind) == "INPUT_OBJECT")
        | "", "arg type \($at)", ($T[$at] | body))
  else
    $T[$n] | "\(.kind | short) \(.name)", body
  end

