package provider

// Outputs contract runtime (Pass 5 Lane 0). reference/CONTRACT.md defines,
// per inventory item, the raw / structured / result / identity / version /
// hash outputs; tools/contractgen turns reference/inventory.json into
// contract_gen.go (the contractItems table, the version pins and one
// embeddable model struct per item). This file builds Terraform schema
// attributes from that table and fills the model structs.
//
// How a lane uses it (data source example; resources and ephemeral
// resources are the same with their own schema package):
//
//	type fooModel struct {
//		ID   types.String `tfsdk:"id"`   // identity outputs stay lane-owned
//		Name types.String `tfsdk:"name"`
//		contractEngineContainerData       // raw + structured + hash + version outputs
//	}
//
//	Schema: Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
//		"id": ..., "name": ...,
//	}, "engine:container-data"),
//
//	Read:   m.contractFill(ctx, d.pd, contractFillInput{
//		Raw:        map[string][]byte{"inspect_json": rawBody},
//		Structured: map[string]any{"inspect": inspectResponse}, // or omit: decoded from inspect_json
//	})
//	resp.State.Set(ctx, &m)
//
//	Resource ModifyPlan (or Create/Update before the API call): m.contractSetUnknown()
//
// Model mechanism: terraform-plugin-framework v1.19 reflection supports
// embedded (anonymous, non-pointer) struct fields in tfsdk models; their
// tfsdk-tagged fields are promoted (internal/reflect/helpers.go
// getStructTags). The embedded struct type may be unexported. A promoted tag
// that duplicates one of the lane's own tags is a runtime error, so a lane
// drops its own field for any output the contract now owns.
//
// A zero-valued contract struct holds untyped nulls for object / list / map
// outputs, which the framework cannot convert. Always call contractFill,
// contractSetUnknown or contractSetNull before State.Set.
//
// Node rules (how a tf_type + fields tree becomes a Terraform type):
//   - string / number / bool: String / Number / Bool attributes
//     (types.String / types.Number / types.Bool).
//   - object with fields: a SingleNestedAttribute (types.Object);
//     list/set/map of such objects: List/Set/MapNestedAttribute.
//   - Fallback for an object node with no nested fields (fields_complete
//     false below that pointer) and for an explicit `dynamic` node:
//     a DynamicAttribute (types.Dynamic) when the node has no list/set/map
//     ancestor (and is not itself an element); otherwise a String attribute
//     holding canonical JSON (keys sorted, snake_case by the mechanical rule).
//     Reason: terraform-plugin-framework v1.19 rejects a dynamic type
//     anywhere below a list, set or map (internal/fwtype
//     static_collection_validation.go, checked by every schema type's
//     ValidateImplementation).
//   - A collection whose element is not an object (list(string),
//     map(list(object)), list(list(string))...) is a plain List/Set/Map
//     attribute. It cannot mark single fields, so it is Sensitive as a whole
//     when any field below it is.
//   - Sensitivity: a raw output flagged sensitive is a Sensitive string; a
//     structured/result output marks only its secret fields (and everything
//     below them); an output that is sensitive as a whole (join tokens) is
//     Sensitive at the top. Hash and version outputs are never sensitive.
//
// Upstream values: a structured or result value can be any JSON-shaped Go
// value (map[string]any, []any, scalars, json.RawMessage, []byte holding
// JSON) or a moby/compose-go struct; it is round-tripped through
// encoding/json. For each field the value is looked up by the field's
// upstream key, then by its snake_case name, then by any key whose
// mechanical snake_case equals the name. Only schema field names are
// renamed: map keys (labels, options, driver opts, sysctls, log config...)
// and every key inside a fallback node pass through untouched (Pass 6c;
// before, fallback keys were snake_cased and max-file became max_file).
// Structured outputs listed in tools/contractgen/swagger_map.json take their
// whole shape from the Engine API swagger, so they have no fallback node
// except where the swagger itself is free-form.

//go:generate go run ../../tools/contractgen -inventory ../../reference/inventory.json -out contract_gen.go

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	eschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// Output categories (reference/CONTRACT.md).
const (
	contractRaw        = "raw"
	contractStructured = "structured"
	contractResult     = "result"
	contractIdentity   = "identity"
	contractVersion    = "version"
	contractHash       = "hash"
)

// contractItem is one inventory item's outputs contract (generated).
type contractItem struct {
	ID      string // inventory id, e.g. "engine:container"
	Kind    string // resource, data_source, action, dockerfile_block, ephemeral_resource, list_resource, function
	Name    string // Terraform type name, e.g. "docker_container"
	MinAPI  string // item-level Engine API minimum ("" = none); see pd.gateItemAPI
	Outputs []contractOutput
	Inputs  []contractInputSpec
}

// contractOutput is one output (generated).
type contractOutput struct {
	Name            string
	Category        string
	TFType          string
	Sensitive       bool
	KnownAfterApply bool
	Description     string
	HashOf          string // hash outputs: the raw output hashed
	Serializes      string // raw outputs: the structured output serialized
	SensitiveReason string
	FieldsComplete  bool
	Fields          []contractField
}

// contractField is one node of an output's upstream -> snake_case map.
type contractField struct {
	Name        string
	Upstream    string
	TFType      string
	Sensitive   bool
	Description string
	Fields      []contractField
}

// contractInputSpec is an argument recorded on the item (the logs bounds).
// Default is the JSON default ("" = none).
type contractInputSpec struct {
	Name, TFType, Default, Upstream, Description string
}

// contractModel is implemented by every generated model struct.
type contractModel interface {
	contractItemID() string
	contractAssign(map[string]attr.Value)
}

// contractLookup returns the contract of itemID.
func contractLookup(itemID string) (*contractItem, bool) {
	it, ok := contractItems[itemID]
	return it, ok
}

// contractMustItem returns the contract of itemID, panicking on an unknown
// id (a programming error every unit test of the calling type catches).
func contractMustItem(itemID string) *contractItem {
	it, ok := contractItems[itemID]
	if !ok {
		panic(fmt.Sprintf("outputs contract: unknown item id %q (regenerate contract_gen.go?)", itemID))
	}
	return it
}

// contractOutputNames lists the names of itemID's outputs in the given
// categories (all categories when none is given), in inventory order.
func contractOutputNames(itemID string, categories ...string) []string {
	it := contractMustItem(itemID)
	var out []string
	for _, o := range it.Outputs {
		if len(categories) == 0 || contractIn(o.Category, categories) {
			out = append(out, o.Name)
		}
	}
	return out
}

// contractIdentityOutputNames lists itemID's identity outputs. The contract
// does not generate them: the lane owns them as ordinary attributes, and a
// test checks with this list that the lane's schema has each one.
func contractIdentityOutputNames(itemID string) []string {
	return contractOutputNames(itemID, contractIdentity)
}

// contractModelOutput reports whether an output is part of an item's
// schema / model struct: raw, structured, hash and version outputs, plus
// result outputs of non-action items. Must match tools/contractgen.
func contractModelOutput(kind string, o contractOutput) bool {
	switch o.Category {
	case contractRaw, contractStructured, contractHash, contractVersion:
		return true
	case contractResult:
		return kind != "action"
	}
	return false
}

func contractIn(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// tf_type expressions

// contractTypeExpr is a parsed tf_type: string | number | bool | dynamic |
// list(T) | set(T) | map(T) | object({...}) | object({a=T, b=T}).
type contractTypeExpr struct {
	head   string
	elem   *contractTypeExpr
	attrs  []contractNamedExpr // explicit object attributes
	opaque bool                // object({...}): attributes come from fields
}

type contractNamedExpr struct {
	name string
	expr *contractTypeExpr
}

// contractParseType parses a tf_type expression.
func contractParseType(s string) (*contractTypeExpr, error) {
	p := &contractTypeParser{s: s}
	e, err := p.parse()
	if err != nil {
		return nil, fmt.Errorf("tf_type %q: %w", s, err)
	}
	p.space()
	if p.i != len(p.s) {
		return nil, fmt.Errorf("tf_type %q: trailing text at %d", s, p.i)
	}
	return e, nil
}

type contractTypeParser struct {
	s string
	i int
}

func (p *contractTypeParser) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n') {
		p.i++
	}
}

func (p *contractTypeParser) ident() string {
	p.space()
	j := p.i
	for p.i < len(p.s) && (p.s[p.i] == '_' || p.s[p.i] >= 'a' && p.s[p.i] <= 'z' || p.s[p.i] >= 'A' && p.s[p.i] <= 'Z' || p.s[p.i] >= '0' && p.s[p.i] <= '9') {
		p.i++
	}
	return p.s[j:p.i]
}

func (p *contractTypeParser) expect(c byte) error {
	p.space()
	if p.i >= len(p.s) || p.s[p.i] != c {
		return fmt.Errorf("want %q at %d", c, p.i)
	}
	p.i++
	return nil
}

func (p *contractTypeParser) parse() (*contractTypeExpr, error) {
	head := p.ident()
	switch head {
	case "string", "number", "bool", "dynamic":
		return &contractTypeExpr{head: head}, nil
	case "list", "set", "map":
		if err := p.expect('('); err != nil {
			return nil, err
		}
		el, err := p.parse()
		if err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		return &contractTypeExpr{head: head, elem: el}, nil
	case "object":
		if err := p.expect('('); err != nil {
			return nil, err
		}
		if err := p.expect('{'); err != nil {
			return nil, err
		}
		e := &contractTypeExpr{head: "object"}
		p.space()
		if strings.HasPrefix(p.s[p.i:], "...") {
			p.i += 3
			e.opaque = true
		} else {
			for {
				p.space()
				if p.i < len(p.s) && p.s[p.i] == '}' {
					break
				}
				name := p.ident()
				if name == "" {
					return nil, fmt.Errorf("want attribute name at %d", p.i)
				}
				if err := p.expect('='); err != nil {
					return nil, err
				}
				at, err := p.parse()
				if err != nil {
					return nil, err
				}
				e.attrs = append(e.attrs, contractNamedExpr{name: name, expr: at})
				p.space()
				if p.i < len(p.s) && p.s[p.i] == ',' {
					p.i++
				}
			}
		}
		if err := p.expect('}'); err != nil {
			return nil, err
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		return e, nil
	}
	return nil, fmt.Errorf("unknown type %q at %d", head, p.i)
}

// ---------------------------------------------------------------------------
// Nodes

type contractNodeKind int

const (
	cnString contractNodeKind = iota
	cnNumber
	cnBool
	cnDynamic // fallback without a collection ancestor
	cnJSON    // fallback below a collection: canonical JSON string
	cnList
	cnSet
	cnMap
	cnObject
)

// contractNode is a resolved type node of one output.
type contractNode struct {
	Kind        contractNodeKind
	Elem        *contractNode   // list / set / map
	Attrs       []contractAttr  // object, in field order
	Sensitive   bool            // this node is a secret channel (inherited)
	Description string          // MarkdownDescription of the attribute this node becomes
	attrType    attr.Type       // cached
	byName      map[string]bool // object attribute names
}

// contractAttr is one attribute of an object node.
type contractAttr struct {
	Name     string
	Upstream string
	Node     *contractNode
}

// contractBuildNode resolves an expression + fields tree into a node.
func contractBuildNode(e *contractTypeExpr, fields []contractField, sensitive, inCollection bool, desc string) (*contractNode, error) {
	n := &contractNode{Sensitive: sensitive, Description: desc}
	switch e.head {
	case "string":
		n.Kind = cnString
	case "number":
		n.Kind = cnNumber
	case "bool":
		n.Kind = cnBool
	case "dynamic":
		n.Kind = contractFallbackKind(inCollection)
	case "list", "set", "map":
		n.Kind = map[string]contractNodeKind{"list": cnList, "set": cnSet, "map": cnMap}[e.head]
		el, err := contractBuildNode(e.elem, fields, sensitive, true, "")
		if err != nil {
			return nil, err
		}
		n.Elem = el
	case "object":
		switch {
		case len(fields) > 0:
			n.Kind = cnObject
			for _, f := range fields {
				fe, err := contractParseType(f.TFType)
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", f.Name, err)
				}
				child, err := contractBuildNode(fe, f.Fields, sensitive || f.Sensitive, inCollection, contractFieldDescription(f))
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", f.Name, err)
				}
				n.Attrs = append(n.Attrs, contractAttr{Name: f.Name, Upstream: f.Upstream, Node: child})
			}
		case len(e.attrs) > 0:
			n.Kind = cnObject
			for _, a := range e.attrs {
				child, err := contractBuildNode(a.expr, nil, sensitive, inCollection, "The `"+a.name+"` value.")
				if err != nil {
					return nil, err
				}
				n.Attrs = append(n.Attrs, contractAttr{Name: a.name, Node: child})
			}
		case !e.opaque:
			// object({}): an empty object (swagger `enum: [{}]`, e.g. the
			// values of ExposedPorts and Volumes).
			n.Kind = cnObject
		default:
			n.Kind = contractFallbackKind(inCollection)
		}
	default:
		return nil, fmt.Errorf("unsupported type %q", e.head)
	}
	if n.Kind == cnObject {
		n.byName = map[string]bool{}
		for _, a := range n.Attrs {
			if n.byName[a.Name] {
				return nil, fmt.Errorf("duplicate field name %q", a.Name)
			}
			n.byName[a.Name] = true
		}
	}
	if n.Elem != nil && n.Elem.jsonLeaf() {
		n.Description = strings.TrimSpace(n.Description + " Each element is canonical JSON (keys sorted, as upstream): no nested fields are fixed here and Terraform allows no dynamic value inside a list, set or map.")
	}
	switch n.Kind {
	case cnDynamic:
		n.Description = strings.TrimSpace(n.Description + " Dynamic value: the upstream value with its keys as upstream (no nested fields are fixed here).")
	case cnJSON:
		n.Description = strings.TrimSpace(n.Description + " Canonical JSON (keys sorted, as upstream): no nested fields are fixed here and Terraform allows no dynamic value inside a list, set or map.")
	}
	return n, nil
}

func contractFallbackKind(inCollection bool) contractNodeKind {
	if inCollection {
		return cnJSON
	}
	return cnDynamic
}

func contractFieldDescription(f contractField) string {
	d := strings.TrimSpace(f.Description)
	if d == "" {
		d = "Upstream `" + strings.ReplaceAll(f.Upstream, "`", "'") + "`."
	} else if !strings.HasSuffix(d, ".") {
		d += "."
	}
	if f.Sensitive {
		d += " Sensitive."
	}
	return d
}

// jsonLeaf reports whether n is a JSON fallback, directly or as the
// element of nested collections.
func (n *contractNode) jsonLeaf() bool {
	for ; n != nil; n = n.Elem {
		if n.Kind == cnJSON {
			return true
		}
	}
	return false
}

// anySensitive reports whether n or any node below it is sensitive.
func (n *contractNode) anySensitive() bool {
	if n.Sensitive {
		return true
	}
	if n.Elem != nil && n.Elem.anySensitive() {
		return true
	}
	for _, a := range n.Attrs {
		if a.Node.anySensitive() {
			return true
		}
	}
	return false
}

// AttrType is the framework type of values of n.
func (n *contractNode) AttrType() attr.Type {
	if n.attrType != nil {
		return n.attrType
	}
	var t attr.Type
	switch n.Kind {
	case cnString, cnJSON:
		t = types.StringType
	case cnNumber:
		t = types.NumberType
	case cnBool:
		t = types.BoolType
	case cnDynamic:
		t = types.DynamicType
	case cnList:
		t = types.ListType{ElemType: n.Elem.AttrType()}
	case cnSet:
		t = types.SetType{ElemType: n.Elem.AttrType()}
	case cnMap:
		t = types.MapType{ElemType: n.Elem.AttrType()}
	case cnObject:
		t = types.ObjectType{AttrTypes: n.objectAttrTypes()}
	}
	n.attrType = t
	return t
}

func (n *contractNode) objectAttrTypes() map[string]attr.Type {
	m := make(map[string]attr.Type, len(n.Attrs))
	for _, a := range n.Attrs {
		m[a.Name] = a.Node.AttrType()
	}
	return m
}

// nested reports whether n becomes a nested attribute (object, or a
// collection of objects).
func (n *contractNode) nested() bool {
	return n.Kind == cnObject || (n.Kind == cnList || n.Kind == cnSet || n.Kind == cnMap) && n.Elem.Kind == cnObject
}

var (
	contractNodeMu    sync.Mutex
	contractNodeCache = map[string]*contractNode{}
)

// contractOutputNode resolves (and caches) the node of one output.
func contractOutputNode(it *contractItem, o contractOutput) *contractNode {
	key := it.ID + "\x00" + o.Name
	contractNodeMu.Lock()
	defer contractNodeMu.Unlock()
	if n, ok := contractNodeCache[key]; ok {
		return n
	}
	e, err := contractParseType(o.TFType)
	if err != nil {
		panic(fmt.Sprintf("outputs contract %s.%s: %v", it.ID, o.Name, err))
	}
	desc := strings.TrimSpace(o.Description)
	if desc == "" {
		desc = fmt.Sprintf("The `%s` %s output.", o.Name, o.Category)
	} else if !strings.HasSuffix(desc, ".") {
		desc += "."
	}
	sens := o.Sensitive && o.Category != contractHash && o.Category != contractVersion
	n, err := contractBuildNode(e, o.Fields, sens, false, desc)
	if err != nil {
		panic(fmt.Sprintf("outputs contract %s.%s: %v", it.ID, o.Name, err))
	}
	// A scalar output carrying fields (a description of its parts) stays scalar.
	// Compute every attr.Type of the tree now, under contractNodeMu, before the node is shared: AttrType caches
	// lazily, and two data sources read concurrently (Terraform reads them in parallel) would otherwise race on
	// n.attrType and could see a half-written interface (Pass 6b: a nil-pointer panic in basetypes.MapType.Equal
	// from data.docker_tasks). After this, shared nodes are only read.
	contractWarmTypes(n)
	contractNodeCache[key] = n
	return n
}

// contractWarmTypes fills the attrType cache of n and of every node below it.
func contractWarmTypes(n *contractNode) {
	if n == nil {
		return
	}
	contractWarmTypes(n.Elem)
	for _, a := range n.Attrs {
		contractWarmTypes(a.Node)
	}
	n.AttrType()
}

// contractNodeTypesWarm reports whether n and every node below it have their attrType cached (tests).
func contractNodeTypesWarm(n *contractNode) bool {
	if n == nil {
		return true
	}
	if n.attrType == nil || !contractNodeTypesWarm(n.Elem) {
		return false
	}
	for _, a := range n.Attrs {
		if !contractNodeTypesWarm(a.Node) {
			return false
		}
	}
	return true
}

// contractModelOutputs returns the outputs that are schema attributes.
func contractModelOutputs(it *contractItem) []contractOutput {
	var out []contractOutput
	for _, o := range it.Outputs {
		if contractModelOutput(it.Kind, o) {
			out = append(out, o)
		}
	}
	return out
}

// contractAttrTypes returns the framework type of every model output.
func contractAttrTypes(itemID string) map[string]attr.Type {
	it := contractMustItem(itemID)
	m := map[string]attr.Type{}
	for _, o := range contractModelOutputs(it) {
		m[o.Name] = contractOutputNode(it, o).AttrType()
	}
	return m
}

// ---------------------------------------------------------------------------
// Schema attributes (all Computed)

// contractResourceAttributes returns the resource/schema attributes of
// itemID's raw, structured, hash and version outputs (and result outputs of
// non-action items). No plan modifiers: they show as known after apply on
// every change; a lane adds UseStateForUnknown where that is wrong.
func contractResourceAttributes(itemID string) map[string]rschema.Attribute {
	it := contractMustItem(itemID)
	out := map[string]rschema.Attribute{}
	for _, o := range contractModelOutputs(it) {
		out[o.Name] = contractResourceAttr(contractOutputNode(it, o))
	}
	return out
}

// contractAddResourceAttributes adds itemID's contract attributes to attrs
// (which may be nil) and returns it. A name clash panics: the lane must drop
// its own attribute for an output the contract owns.
func contractAddResourceAttributes(attrs map[string]rschema.Attribute, itemID string) map[string]rschema.Attribute {
	if attrs == nil {
		attrs = map[string]rschema.Attribute{}
	}
	for k, v := range contractResourceAttributes(itemID) {
		if _, dup := attrs[k]; dup {
			panic(fmt.Sprintf("outputs contract %s: attribute %q is already in the schema", itemID, k))
		}
		attrs[k] = v
	}
	return attrs
}

// contractDataSourceAttributes is contractResourceAttributes for
// datasource/schema.
func contractDataSourceAttributes(itemID string) map[string]dschema.Attribute {
	it := contractMustItem(itemID)
	out := map[string]dschema.Attribute{}
	for _, o := range contractModelOutputs(it) {
		out[o.Name] = contractDataSourceAttr(contractOutputNode(it, o))
	}
	return out
}

// contractAddDataSourceAttributes is contractAddResourceAttributes for
// datasource/schema.
func contractAddDataSourceAttributes(attrs map[string]dschema.Attribute, itemID string) map[string]dschema.Attribute {
	if attrs == nil {
		attrs = map[string]dschema.Attribute{}
	}
	for k, v := range contractDataSourceAttributes(itemID) {
		if _, dup := attrs[k]; dup {
			panic(fmt.Sprintf("outputs contract %s: attribute %q is already in the schema", itemID, k))
		}
		attrs[k] = v
	}
	return attrs
}

// contractEphemeralAttributes is contractResourceAttributes for
// ephemeral/schema.
func contractEphemeralAttributes(itemID string) map[string]eschema.Attribute {
	it := contractMustItem(itemID)
	out := map[string]eschema.Attribute{}
	for _, o := range contractModelOutputs(it) {
		out[o.Name] = contractEphemeralAttr(contractOutputNode(it, o))
	}
	return out
}

// contractAddEphemeralAttributes is contractAddResourceAttributes for
// ephemeral/schema.
func contractAddEphemeralAttributes(attrs map[string]eschema.Attribute, itemID string) map[string]eschema.Attribute {
	if attrs == nil {
		attrs = map[string]eschema.Attribute{}
	}
	for k, v := range contractEphemeralAttributes(itemID) {
		if _, dup := attrs[k]; dup {
			panic(fmt.Sprintf("outputs contract %s: attribute %q is already in the schema", itemID, k))
		}
		attrs[k] = v
	}
	return attrs
}

func contractResourceAttr(n *contractNode) rschema.Attribute {
	d := n.Description
	switch n.Kind {
	case cnString, cnJSON:
		return rschema.StringAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnNumber:
		return rschema.NumberAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnBool:
		return rschema.BoolAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnDynamic:
		return rschema.DynamicAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnObject:
		return rschema.SingleNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, Attributes: contractResourceChildren(n)}
	}
	if n.Elem.Kind == cnObject {
		no := rschema.NestedAttributeObject{Attributes: contractResourceChildren(n.Elem)}
		switch n.Kind {
		case cnList:
			return rschema.ListNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		case cnSet:
			return rschema.SetNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		default:
			return rschema.MapNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		}
	}
	et, s := n.Elem.AttrType(), n.anySensitive()
	switch n.Kind {
	case cnList:
		return rschema.ListAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	case cnSet:
		return rschema.SetAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	default:
		return rschema.MapAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	}
}

func contractResourceChildren(n *contractNode) map[string]rschema.Attribute {
	m := make(map[string]rschema.Attribute, len(n.Attrs))
	for _, a := range n.Attrs {
		m[a.Name] = contractResourceAttr(a.Node)
	}
	return m
}

func contractDataSourceAttr(n *contractNode) dschema.Attribute {
	d := n.Description
	switch n.Kind {
	case cnString, cnJSON:
		return dschema.StringAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnNumber:
		return dschema.NumberAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnBool:
		return dschema.BoolAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnDynamic:
		return dschema.DynamicAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnObject:
		return dschema.SingleNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, Attributes: contractDataSourceChildren(n)}
	}
	if n.Elem.Kind == cnObject {
		no := dschema.NestedAttributeObject{Attributes: contractDataSourceChildren(n.Elem)}
		switch n.Kind {
		case cnList:
			return dschema.ListNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		case cnSet:
			return dschema.SetNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		default:
			return dschema.MapNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		}
	}
	et, s := n.Elem.AttrType(), n.anySensitive()
	switch n.Kind {
	case cnList:
		return dschema.ListAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	case cnSet:
		return dschema.SetAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	default:
		return dschema.MapAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	}
}

func contractDataSourceChildren(n *contractNode) map[string]dschema.Attribute {
	m := make(map[string]dschema.Attribute, len(n.Attrs))
	for _, a := range n.Attrs {
		m[a.Name] = contractDataSourceAttr(a.Node)
	}
	return m
}

func contractEphemeralAttr(n *contractNode) eschema.Attribute {
	d := n.Description
	switch n.Kind {
	case cnString, cnJSON:
		return eschema.StringAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnNumber:
		return eschema.NumberAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnBool:
		return eschema.BoolAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnDynamic:
		return eschema.DynamicAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d}
	case cnObject:
		return eschema.SingleNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, Attributes: contractEphemeralChildren(n)}
	}
	if n.Elem.Kind == cnObject {
		no := eschema.NestedAttributeObject{Attributes: contractEphemeralChildren(n.Elem)}
		switch n.Kind {
		case cnList:
			return eschema.ListNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		case cnSet:
			return eschema.SetNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		default:
			return eschema.MapNestedAttribute{Computed: true, Sensitive: n.Sensitive, MarkdownDescription: d, NestedObject: no}
		}
	}
	et, s := n.Elem.AttrType(), n.anySensitive()
	switch n.Kind {
	case cnList:
		return eschema.ListAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	case cnSet:
		return eschema.SetAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	default:
		return eschema.MapAttribute{Computed: true, Sensitive: s, MarkdownDescription: d, ElementType: et}
	}
}

func contractEphemeralChildren(n *contractNode) map[string]eschema.Attribute {
	m := make(map[string]eschema.Attribute, len(n.Attrs))
	for _, a := range n.Attrs {
		m[a.Name] = contractEphemeralAttr(a.Node)
	}
	return m
}

// ---------------------------------------------------------------------------
// snake_case

var (
	contractIdentRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-]*$`)
	contractSnakeFixups = strings.NewReplacer("SELinux", "Selinux", "IOps", "Iops")
)

// contractSnake is the CONTRACT.md mechanical snake_case rule:
// HostConfig -> host_config, IPAddress -> ip_address, ID -> id,
// EnableIPv6 -> enable_ipv6, LinkLocalIPs -> link_local_ips,
// NanoCPUs -> nano_cpus, SELinuxContext -> selinux_context, hyphens become
// underscores. A string that is not an identifier (a label key such as
// com.docker.compose.project, a path, an image reference) is returned
// unchanged.
func contractSnake(s string) string {
	if !contractIdentRe.MatchString(s) {
		return s
	}
	s = contractSnakeFixups.Replace(strings.ReplaceAll(s, "-", "_"))
	r := []rune(s)
	n := len(r)
	isUp := func(c rune) bool { return c >= 'A' && c <= 'Z' }
	isLow := func(c rune) bool { return c >= 'a' && c <= 'z' }
	isDig := func(c rune) bool { return c >= '0' && c <= '9' }
	var words []string
	for i := 0; i < n; {
		c := r[i]
		switch {
		case c == '_':
			i++
		case isUp(c):
			j := i
			for j < n && isUp(r[j]) {
				j++
			}
			run := string(r[i:j])
			if j < n && isLow(r[j]) {
				switch {
				case j-i > 1 && r[j] == 's' && (j+1 == n || !isLow(r[j+1])):
					// Acronym plural: IPs, CPUs.
					words = append(words, run+"s")
					i = j + 1
				case j-i > 1 && r[j] == 'v' && j+1 < n && isDig(r[j+1]):
					// IPv6, IPv4.
					k := j + 1
					for k < n && isDig(r[k]) {
						k++
					}
					words = append(words, run+string(r[j:k]))
					i = k
				case j-i > 1:
					// IPAddress: the last capital starts the next word.
					words = append(words, string(r[i:j-1]))
					i = j - 1
				default:
					k := j
					for k < n && (isLow(r[k]) || isDig(r[k])) {
						k++
					}
					words = append(words, string(r[i:k]))
					i = k
				}
			} else {
				k := j
				for k < n && isDig(r[k]) {
					k++
				}
				words = append(words, string(r[i:k]))
				i = k
			}
		default:
			k := i
			for k < n && !isUp(r[k]) && r[k] != '_' {
				k++
			}
			words = append(words, string(r[i:k]))
			i = k
		}
	}
	return strings.ToLower(strings.Join(words, "_"))
}

// contractSnakeKeys returns v with every object key converted by
// contractSnake, recursively (the rule for unmapped levels).
func contractSnakeKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[contractSnake(k)] = contractSnakeKeys(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = contractSnakeKeys(x)
		}
		return out
	}
	return v
}

// ---------------------------------------------------------------------------
// Normalizing upstream values

// contractPlain round-trips v through encoding/json into map[string]any /
// []any / json.Number / string / bool / nil. []byte and json.RawMessage are
// decoded as JSON.
func contractPlain(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	var b []byte
	switch t := v.(type) {
	case json.RawMessage:
		b = t
	case []byte:
		b = t
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out any
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// contractNormalize maps a plain value onto node n: object keys become the
// contract's snake_case names, fallback nodes keep their keys, and
// scalars are coerced to the node's type. Shape mismatches become null and
// a call to warn with the path.
func contractNormalize(n *contractNode, v any, p string, warn func(string)) any {
	if v == nil {
		return nil
	}
	switch n.Kind {
	case cnString:
		switch t := v.(type) {
		case string:
			return t
		case json.Number:
			return t.String()
		case bool:
			return strconv.FormatBool(t)
		default:
			b, _ := json.Marshal(t)
			return string(b)
		}
	case cnNumber:
		switch t := v.(type) {
		case json.Number:
			return t
		case string:
			if _, ok := new(big.Float).SetString(t); ok {
				return json.Number(t)
			}
		}
		warn(fmt.Sprintf("%s: want a number, got %T", p, v))
		return nil
	case cnBool:
		switch t := v.(type) {
		case bool:
			return t
		case string:
			if b, err := strconv.ParseBool(t); err == nil {
				return b
			}
		}
		warn(fmt.Sprintf("%s: want a bool, got %T", p, v))
		return nil
	case cnDynamic, cnJSON:
		// No schema below this node: keys are user data or unmapped, and only
		// schema field names are ever renamed (Pass 6c), so they pass through.
		return v
	case cnList, cnSet:
		arr, ok := v.([]any)
		if !ok {
			warn(fmt.Sprintf("%s: want a list, got %T", p, v))
			return nil
		}
		out := make([]any, len(arr))
		for i, x := range arr {
			out[i] = contractNormalize(n.Elem, x, fmt.Sprintf("%s[%d]", p, i), warn)
		}
		return out
	case cnMap:
		m, ok := v.(map[string]any)
		if !ok {
			warn(fmt.Sprintf("%s: want a map, got %T", p, v))
			return nil
		}
		out := make(map[string]any, len(m))
		for k, x := range m {
			out[k] = contractNormalize(n.Elem, x, fmt.Sprintf("%s[%q]", p, k), warn)
		}
		return out
	case cnObject:
		m, ok := v.(map[string]any)
		if !ok {
			warn(fmt.Sprintf("%s: want an object, got %T", p, v))
			return nil
		}
		out := make(map[string]any, len(n.Attrs))
		for _, a := range n.Attrs {
			x, _ := contractLookupKey(m, a)
			out[a.Name] = contractNormalize(a.Node, x, p+"."+a.Name, warn)
		}
		return out
	}
	return nil
}

// contractLookupKey finds a's value in m: by upstream key, by snake_case
// name, then by any key whose contractSnake equals the name (keys tried in
// sorted order).
func contractLookupKey(m map[string]any, a contractAttr) (any, bool) {
	if a.Upstream != "" {
		if v, ok := m[a.Upstream]; ok {
			return v, true
		}
	}
	if v, ok := m[a.Name]; ok {
		return v, true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if contractSnake(k) == a.Name {
			return m[k], true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Normalized value -> framework value

// contractToAttr converts a normalized value of node n to its framework value.
func contractToAttr(n *contractNode, v any) (attr.Value, diag.Diagnostics) {
	var diags diag.Diagnostics
	if v == nil {
		return contractNull(n), diags
	}
	switch n.Kind {
	case cnString:
		s, _ := v.(string)
		return types.StringValue(s), diags
	case cnJSON:
		if s, ok := v.(string); ok {
			// A string is stored as is, never JSON-encoded a second time
			// (Pass 6c: mounts[].type held "\"volume\"").
			return types.StringValue(s), diags
		}
		b, err := json.Marshal(v)
		if err != nil {
			diags.AddError("Outputs contract", "encoding JSON: "+err.Error())
			return types.StringNull(), diags
		}
		return types.StringValue(string(b)), diags
	case cnNumber:
		f, _, err := big.ParseFloat(string(v.(json.Number)), 10, 512, big.ToNearestEven)
		if err != nil {
			diags.AddError("Outputs contract", "parsing number: "+err.Error())
			return types.NumberNull(), diags
		}
		return types.NumberValue(f), diags
	case cnBool:
		return types.BoolValue(v.(bool)), diags
	case cnDynamic:
		dv, d := contractDynamic(v)
		diags.Append(d...)
		if dv == nil {
			return types.DynamicNull(), diags
		}
		return types.DynamicValue(dv), diags
	case cnList, cnSet:
		arr := v.([]any)
		elems := make([]attr.Value, 0, len(arr))
		for _, x := range arr {
			ev, d := contractToAttr(n.Elem, x)
			diags.Append(d...)
			elems = append(elems, ev)
		}
		if n.Kind == cnSet {
			sv, d := types.SetValue(n.Elem.AttrType(), elems)
			diags.Append(d...)
			return sv, diags
		}
		lv, d := types.ListValue(n.Elem.AttrType(), elems)
		diags.Append(d...)
		return lv, diags
	case cnMap:
		m := v.(map[string]any)
		elems := make(map[string]attr.Value, len(m))
		for k, x := range m {
			ev, d := contractToAttr(n.Elem, x)
			diags.Append(d...)
			elems[k] = ev
		}
		mv, d := types.MapValue(n.Elem.AttrType(), elems)
		diags.Append(d...)
		return mv, diags
	case cnObject:
		m := v.(map[string]any)
		vals := make(map[string]attr.Value, len(n.Attrs))
		for _, a := range n.Attrs {
			av, d := contractToAttr(a.Node, m[a.Name])
			diags.Append(d...)
			vals[a.Name] = av
		}
		ov, d := types.ObjectValue(n.objectAttrTypes(), vals)
		diags.Append(d...)
		return ov, diags
	}
	return contractNull(n), diags
}

// contractDynamic builds the underlying value of a dynamic node: objects
// (null members dropped), tuples (null elements become null strings),
// numbers, strings and bools.
func contractDynamic(v any) (attr.Value, diag.Diagnostics) {
	var diags diag.Diagnostics
	switch t := v.(type) {
	case nil:
		return nil, diags
	case string:
		return types.StringValue(t), diags
	case bool:
		return types.BoolValue(t), diags
	case json.Number:
		f, _, err := big.ParseFloat(string(t), 10, 512, big.ToNearestEven)
		if err != nil {
			diags.AddError("Outputs contract", "parsing number: "+err.Error())
			return nil, diags
		}
		return types.NumberValue(f), diags
	case []any:
		ts := make([]attr.Type, 0, len(t))
		vs := make([]attr.Value, 0, len(t))
		for _, x := range t {
			ev, d := contractDynamic(x)
			diags.Append(d...)
			if ev == nil {
				ev = types.StringNull()
			}
			ts = append(ts, ev.Type(context.Background()))
			vs = append(vs, ev)
		}
		tv, d := basetypes.NewTupleValue(ts, vs)
		diags.Append(d...)
		return tv, diags
	case map[string]any:
		ts := map[string]attr.Type{}
		vs := map[string]attr.Value{}
		for k, x := range t {
			ev, d := contractDynamic(x)
			diags.Append(d...)
			if ev == nil {
				continue
			}
			ts[k] = ev.Type(context.Background())
			vs[k] = ev
		}
		ov, d := types.ObjectValue(ts, vs)
		diags.Append(d...)
		return ov, diags
	}
	diags.AddError("Outputs contract", fmt.Sprintf("unsupported dynamic value %T", v))
	return nil, diags
}

// contractNull is the typed null of node n.
func contractNull(n *contractNode) attr.Value {
	switch n.Kind {
	case cnString, cnJSON:
		return types.StringNull()
	case cnNumber:
		return types.NumberNull()
	case cnBool:
		return types.BoolNull()
	case cnDynamic:
		return types.DynamicNull()
	case cnList:
		return types.ListNull(n.Elem.AttrType())
	case cnSet:
		return types.SetNull(n.Elem.AttrType())
	case cnMap:
		return types.MapNull(n.Elem.AttrType())
	}
	return types.ObjectNull(n.objectAttrTypes())
}

// contractUnknown is the typed unknown of node n.
func contractUnknown(n *contractNode) attr.Value {
	switch n.Kind {
	case cnString, cnJSON:
		return types.StringUnknown()
	case cnNumber:
		return types.NumberUnknown()
	case cnBool:
		return types.BoolUnknown()
	case cnDynamic:
		return types.DynamicUnknown()
	case cnList:
		return types.ListUnknown(n.Elem.AttrType())
	case cnSet:
		return types.SetUnknown(n.Elem.AttrType())
	case cnMap:
		return types.MapUnknown(n.Elem.AttrType())
	}
	return types.ObjectUnknown(n.objectAttrTypes())
}

// contractUnknownValues returns an unknown value for every model output of
// itemID: the plan-time value of a resource's contract outputs.
func contractUnknownValues(itemID string) map[string]attr.Value {
	it := contractMustItem(itemID)
	out := map[string]attr.Value{}
	for _, o := range contractModelOutputs(it) {
		out[o.Name] = contractUnknown(contractOutputNode(it, o))
	}
	return out
}

// contractUnknownValue is the plan-time (unknown) value of one output, for
// lanes that mark outputs unknown one by one in ModifyPlan.
func contractUnknownValue(itemID, output string) attr.Value {
	it := contractMustItem(itemID)
	for _, o := range it.Outputs {
		if o.Name == output {
			return contractUnknown(contractOutputNode(it, o))
		}
	}
	panic(fmt.Sprintf("outputs contract %s: no output %q", itemID, output))
}

// contractNullValues returns a typed null for every model output of itemID.
func contractNullValues(itemID string) map[string]attr.Value {
	it := contractMustItem(itemID)
	out := map[string]attr.Value{}
	for _, o := range contractModelOutputs(it) {
		out[o.Name] = contractNull(contractOutputNode(it, o))
	}
	return out
}

// ---------------------------------------------------------------------------
// Fill

// contractFillInput is what a lane hands the contract for one item.
type contractFillInput struct {
	// Raw holds the bytes of each raw output by name, e.g. "inspect_json":
	// the exact Engine API response body, "logs": logRender(entries).
	// A *_json raw output that is missing is serialized from its
	// structured output (rCanonicalJSON of the upstream value: keys
	// sorted, numbers as encoded; see r_rawjson.go).
	Raw map[string][]byte
	// Structured holds the upstream value of each structured output by
	// name (a moby struct, map[string]any, json.RawMessage...). A missing
	// one is decoded from the *_json raw output that serializes it.
	Structured map[string]any
	// Result holds result output values by name (non-action items; for
	// actions see actFinishResult).
	Result map[string]any
	// Identity holds identity output values; only actFinishResult uses it
	// (identity attributes of resources and data sources are lane-owned).
	Identity map[string]any
	// DockerfileSyntax is the # syntax= value of the Dockerfile ("" means
	// none: dockerfile_syntax becomes contractDockerfileDefaultSyntax).
	DockerfileSyntax string
	// BuildKitVersion is the builder node's BuildKit version ("" = null).
	BuildKitVersion string
}

// contractCompute returns the normalized plain value of every output of it
// that include accepts: raw outputs as strings, hashes computed from the
// raws, version outputs from pd and the pins. Problems with one value
// become warnings and a null.
func contractCompute(ctx context.Context, pd *providerData, it *contractItem, in contractFillInput, include func(contractOutput) bool) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	warn := func(msg string) { contractDropped(&diags, it, msg) }
	// Raw bytes, with the *_json fallback from the structured value.
	raws := map[string][]byte{}
	for _, o := range it.Outputs {
		if o.Category != contractRaw {
			continue
		}
		if b, ok := in.Raw[o.Name]; ok && b != nil {
			raws[o.Name] = b
			continue
		}
		if o.Serializes != "" && strings.HasSuffix(o.Name, "_json") {
			if sv, ok := in.Structured[o.Serializes]; ok && sv != nil {
				b, err := rCanonicalJSON(sv)
				if err != nil {
					warn(fmt.Sprintf("serializing %s for %s: %v", o.Serializes, o.Name, err))
					continue
				}
				raws[o.Name] = b
			}
		}
	}
	out := map[string]any{}
	for _, o := range it.Outputs {
		if !include(o) {
			continue
		}
		switch o.Category {
		case contractRaw:
			if b, ok := raws[o.Name]; ok {
				out[o.Name] = string(b)
			} else {
				out[o.Name] = nil
			}
		case contractHash:
			if o.Name == contractConfigHashOutput {
				out[o.Name] = contractConfigHash(it, raws[o.HashOf], warn)
				continue
			}
			if b, ok := raws[o.HashOf]; ok {
				sum := sha256.Sum256(b)
				out[o.Name] = hex.EncodeToString(sum[:])
			} else {
				out[o.Name] = nil
			}
		case contractVersion:
			out[o.Name] = contractVersionValue(ctx, pd, o.Name, in, warn)
		default:
			var src map[string]any
			switch o.Category {
			case contractStructured:
				src = in.Structured
			case contractResult:
				src = in.Result
			case contractIdentity:
				src = in.Identity
			}
			v, ok := src[o.Name]
			if !ok && o.Category == contractStructured {
				for _, r := range it.Outputs {
					if r.Category == contractRaw && r.Serializes == o.Name && strings.HasSuffix(r.Name, "_json") {
						if b, ok := raws[r.Name]; ok {
							v = json.RawMessage(b)
						}
					}
				}
			}
			plain, err := contractPlain(v)
			if err != nil {
				warn(fmt.Sprintf("%s: %v", o.Name, err))
				out[o.Name] = nil
				continue
			}
			out[o.Name] = contractNormalize(contractOutputNode(it, o), plain, o.Name, warn)
		}
	}
	return out, diags
}

// contractVersionValue returns one version output's value (nil = null).
func contractVersionValue(ctx context.Context, pd *providerData, name string, in contractFillInput, warn func(string)) any {
	switch name {
	case "engine_api_version":
		if pd == nil || pd.Client == nil {
			return nil
		}
		v, err := pd.APIVersion(ctx)
		if err != nil {
			warn("engine_api_version: " + err.Error())
			return nil
		}
		return v
	case "docker_server_version":
		if pd == nil || pd.Client == nil {
			return nil
		}
		v, err := pd.dockerServerVersion(ctx)
		if err != nil {
			warn("docker_server_version: " + err.Error())
			return nil
		}
		return v
	case "compose_spec_version":
		return contractComposeSpecVersion
	case "compose_go_version":
		return contractComposeGoVersion
	case "buildx_version":
		return contractBuildxVersion
	case "dockerfile_syntax":
		if in.DockerfileSyntax != "" {
			return in.DockerfileSyntax
		}
		return contractDockerfileDefaultSyntax
	case "buildkit_version":
		if in.BuildKitVersion != "" {
			return in.BuildKitVersion
		}
		return nil
	}
	// docker_cli_version (never: nothing runs the CLI) and unknown names.
	return nil
}

// contractValues computes the framework value of every model output of
// itemID (raw, structured, hash, version, and result for non-actions).
func contractValues(ctx context.Context, pd *providerData, itemID string, in contractFillInput) (map[string]attr.Value, diag.Diagnostics) {
	it := contractMustItem(itemID)
	plain, diags := contractCompute(ctx, pd, it, in, func(o contractOutput) bool { return contractModelOutput(it.Kind, o) })
	out := make(map[string]attr.Value, len(plain))
	for _, o := range contractModelOutputs(it) {
		v, d := contractToAttr(contractOutputNode(it, o), plain[o.Name])
		diags.Append(d...)
		out[o.Name] = v
	}
	return out, diags
}

// contractFillModel fills a generated model struct. The generated
// contractFill methods call it; lanes call m.contractFill.
func contractFillModel(ctx context.Context, pd *providerData, m contractModel, in contractFillInput) diag.Diagnostics {
	vals, diags := contractValues(ctx, pd, m.contractItemID(), in)
	if diags.HasError() {
		vals = contractNullValues(m.contractItemID())
	}
	m.contractAssign(vals)
	return diags
}
