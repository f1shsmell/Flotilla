// Package wirecontract renders the Go wire types into the one TypeScript module
// the GUI reads. It exists because the same facts were being written twice: a
// method name, a JSON tag and a frame constant were retyped in the GUI, and a
// rename on either side stayed silent until a call failed at run time.
//
// The contract holds the facts a client branches on — method names, constants
// and the JSON shape of every type that crosses the wire. It is derived from the
// source by parsing it, so there is no registry to keep in step, and `-check`
// turns a stale file into a failing build.
package wirecontract

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Packages are the source directories the contract covers: the packages whose
// types and constants travel between the Go processes and the GUI.
var Packages = []string{
	"internal/ipc",
	"internal/session",
	"internal/daemon",
	"internal/agent",
	"internal/diff",
	"internal/comments",
	"internal/home",
}

// WireTypes are the structs a client reads. The list is explicit rather than
// derived: a package also holds configuration, error wrappers and process
// records, and putting those in the contract would invite a client to depend on
// them. A type that crosses the wire and is not named here does not exist as far
// as the GUI is concerned, which is the mistake this gate catches.
var WireTypes = []string{
	"ipc.Handshake",
	"session.Info",
	"session.Record",
	"session.StateUpdate",
	"session.UpdateMessage",
	"session.AttachRequest",
	"session.AttachResponse",
	"session.PromptRequest",
	"session.PromptResponse",
	"session.PermissionQuestion",
	"session.PermissionOption",
	"session.PermissionReply",
	"session.ResizeRequest",
	"session.StopRequest",
	"session.ProtectRequest",
	"session.DiffRequest",
	"session.DiffResponse",
	"session.CommentAddRequest",
	"session.CommentRemoveRequest",
	"session.CommentListResponse",
	"session.CommitRequest",
	"session.PullRequestRequest",
	"session.WriteResponse",
	"session.HistoryRequest",
	"session.HistoryResponse",
	"session.QueuedMessage",
	"session.QueueResponse",
	"session.QueueAddRequest",
	"session.QueueEditRequest",
	"session.QueueRemoveRequest",
	"session.QueueMoveRequest",
	"session.SteerRequest",
	"session.SteerDelivery",
	"session.Checkpoint",
	"session.CheckpointListResponse",
	"session.CheckpointRestoreRequest",
	"session.ArchivedEntry",
	"session.ArchiveRequest",
	"session.ArchiveResponse",
	"session.Control",
	"session.Choice",
	"session.SurfaceResponse",
	"session.SurfaceSetRequest",
	"daemon.Status",
	"daemon.StatusEntry",
	"daemon.InboxEntry",
	"daemon.TaskEntry",
	"daemon.StartRequest",
	"daemon.StartResponse",
	"daemon.SetAgentRequest",
	"daemon.WorktreeRequest",
	"agent.Entry",
	"agent.Listing",
	"agent.RegistryEntry",
	"agent.Change",
	"diff.File",
	"comments.Comment",
	"home.Paths",
}

// Field is one JSON field of a wire type.
type Field struct {
	// Name is the JSON name: what the client reads.
	Name string
	// Go is the field's identifier, which names a mismatch in a diff.
	Go string
	// Type is the Go type as written, for a reader who needs the shape.
	Type string
	// Optional is true for a field that may be absent from the JSON.
	Optional bool
}

// Type is one wire type: an exported struct with at least one JSON field.
type Type struct {
	// Key is "package.Type", unique across the contract.
	Key string
	// Fields are sorted by JSON name, so a rewrite of the source does not
	// change the file.
	Fields []Field
}

// Contract is the whole file.
type Contract struct {
	// Methods maps "package.ConstName" to the method string it holds, so a
	// client that renames one call site cannot drift from the Go constant.
	Methods map[string]string
	// Constants maps "package.ConstName" to the literal the source wrote, for
	// the numeric facts a client mirrors: protocol version, frame header, error
	// codes.
	Constants map[string]string
	// Types are the wire structs, sorted by key.
	Types []Type
}

// source holds one package's declarations, so a struct can be resolved by name
// when it is embedded in another and its constants can be read without keeping
// the ASTs alive.
type source struct {
	fset      *token.FileSet
	pkg       string
	types     map[string]*ast.StructType
	decls     []*ast.GenDecl
	typesList []*ast.TypeSpec
}

// Build parses the packages under root and returns the contract they describe.
func Build(root string) (Contract, error) {
	out := Contract{
		Methods:   map[string]string{},
		Constants: map[string]string{},
	}
	parsed := map[string]*source{}

	for _, pkg := range Packages {
		src, err := parse(filepath.Join(root, pkg))
		if err != nil {
			return Contract{}, fmt.Errorf("read %s: %w", pkg, err)
		}
		// Indexed by the package's own name, which is what a wire type's key
		// carries: "ipc.Handshake" names the ipc package, not its directory.
		parsed[src.pkg] = src
		src.collectConsts(&out)
	}

	for _, key := range WireTypes {
		src := parsed[packageOf(key)]
		if src == nil {
			return Contract{}, fmt.Errorf("wire type %q names a package the contract does not read", key)
		}
		st, ok := src.types[typeOf(key)]
		if !ok {
			return Contract{}, fmt.Errorf("wire type %q is not a struct in the source", key)
		}
		fields := src.structFields(st, 0)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		out.Types = append(out.Types, Type{Key: key, Fields: fields})
	}

	sort.Slice(out.Types, func(i, j int) bool { return out.Types[i].Key < out.Types[j].Key })
	return out, nil
}

// parse reads every non-test file of one package directory.
func parse(dir string) (*source, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	src := &source{fset: token.NewFileSet(), types: map[string]*ast.StructType{}}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(src.fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		src.pkg = file.Name.Name

		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			src.decls = append(src.decls, gd)
			if gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				src.typesList = append(src.typesList, ts)
				if st, ok := ts.Type.(*ast.StructType); ok {
					src.types[ts.Name.Name] = st
				}
			}
		}
	}
	if src.pkg == "" {
		return nil, fmt.Errorf("%s holds no Go files", dir)
	}
	return src, nil
}

// collectConsts reads every exported constant in the package. A string constant
// whose name starts with Method is a wire method; every other literal is
// recorded as a value the client may mirror.
//
// A value is rendered the way the source wrote it when the source wrote a
// literal, and as its evaluated decimal form otherwise: `1 << 20` and `-32000`
// are expressions, and a client that mirrors a frame limit or an error code
// needs the number, not the syntax.
func (s *source) collectConsts(out *Contract) {
	for _, decl := range s.decls {
		if decl.Tok != token.CONST {
			continue
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !name.IsExported() || i >= len(vs.Values) {
					// An iota continuation inherits the previous expression,
					// which this contract does not render: its value is a number
					// the client does not mirror.
					continue
				}

				key := s.pkg + "." + name.Name
				expr := vs.Values[i]

				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					if strings.HasPrefix(name.Name, "Method") {
						out.Methods[key] = value
						continue
					}
					out.Constants[key] = value
					continue
				}

				// A literal is already the form the source chose; anything else
				// is evaluated, because a negative code or a shift is an
				// expression and losing it would leave the client with no value
				// at all.
				value, ok := evalConst(expr)
				if !ok {
					continue
				}
				out.Constants[key] = value
			}
		}
	}
}

// evalConst renders a constant expression as a string. It handles the shapes a
// wire constant actually takes — a literal, a negation, a shift or arithmetic
// over literals, and a quoted string — and reports false for anything else,
// which a caller treats as "not a constant a client mirrors".
func evalConst(expr ast.Expr) (string, bool) {
	value, ok := evalNumber(expr)
	if !ok {
		return "", false
	}
	return exactString(value)
}

// exactString renders a constant, refusing one the evaluator could not pin down.
// go/constant represents "I do not know" as Unknown, whose ExactString is the
// word "unknown": writing that into the contract would be a value a client
// silently cannot parse.
func exactString(value constant.Value) (string, bool) {
	if value == nil || value.Kind() == constant.Unknown {
		return "", false
	}
	return value.ExactString(), true
}

// evalNumber evaluates an expression to an integer constant. It reports false
// for anything it cannot pin down, and never panics: go/constant panics on an
// operation it does not define, and a parser walking a whole package must treat
// that as "not a constant this contract carries" rather than a crash.
func evalNumber(expr ast.Expr) (value constant.Value, ok bool) {
	defer func() {
		if recover() != nil {
			value, ok = nil, false
		}
	}()

	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.INT {
			return nil, false
		}
		value := constant.MakeFromLiteral(e.Value, token.INT, 0)
		if value.Kind() == constant.Unknown {
			return nil, false
		}
		return value, true
	case *ast.ParenExpr:
		return evalNumber(e.X)
	case *ast.UnaryExpr:
		value, ok := evalNumber(e.X)
		if !ok {
			return nil, false
		}
		return constant.UnaryOp(e.Op, value, 0), true
	case *ast.BinaryExpr:
		left, ok := evalNumber(e.X)
		if !ok {
			return nil, false
		}
		right, ok := evalNumber(e.Y)
		if !ok {
			return nil, false
		}
		value, ok := binaryValue(left, e.Op, right)
		return value, ok
	}
	return nil, false
}

// binaryValue applies one binary operator. Shift is not an arithmetic operation
// in go/constant and has an entry of its own; a comparison yields a boolean,
// which no wire constant is.
func binaryValue(left constant.Value, op token.Token, right constant.Value) (constant.Value, bool) {
	switch op {
	case token.SHL, token.SHR:
		shift, ok := constant.Uint64Val(constant.ToInt(right))
		if !ok {
			return nil, false
		}
		return constant.Shift(left, op, uint(shift)), true
	case token.LAND, token.LOR, token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return nil, false
	}

	return constant.BinaryOp(left, op, right), true
}

// structFields lists the JSON fields of one struct, flattening an embedded
// struct the way encoding/json promotes its fields.
func (s *source) structFields(st *ast.StructType, depth int) []Field {
	if depth > 4 {
		return nil
	}

	var out []Field
	for _, field := range st.Fields.List {
		tag := ""
		if field.Tag != nil {
			tag = strings.Trim(field.Tag.Value, "`")
		}
		name, optional, skip := jsonTag(tag)
		if skip {
			continue
		}

		if len(field.Names) == 0 {
			// An embedded struct's fields ride the outer type's JSON, unless
			// the field carries a JSON name of its own, which makes it one
			// field like any other.
			if name != "" {
				out = append(out, Field{
					Name:     name,
					Go:       embeddedName(field.Type),
					Type:     render(s.fset, field.Type),
					Optional: optional,
				})
				continue
			}
			if inner := s.embeddedStruct(field.Type); inner != nil {
				out = append(out, s.structFields(inner, depth+1)...)
			}
			continue
		}

		for _, ident := range field.Names {
			if !ident.IsExported() {
				continue
			}
			fieldName := name
			if fieldName == "" {
				fieldName = ident.Name
			}
			out = append(out, Field{
				Name:     fieldName,
				Go:       ident.Name,
				Type:     render(s.fset, field.Type),
				Optional: optional,
			})
		}
	}
	return out
}

// embeddedStruct resolves the struct an embedded field stands for. A type this
// package does not declare is left unexpanded: its fields are not this package's
// to render, and the client learns the shape from the type name.
func (s *source) embeddedStruct(expr ast.Expr) *ast.StructType {
	if ptr, ok := expr.(*ast.StarExpr); ok {
		expr = ptr.X
	}

	if st, ok := expr.(*ast.StructType); ok {
		return st
	}

	ident, ok := expr.(*ast.Ident)
	if !ok {
		return nil
	}
	return s.types[ident.Name]
}

// embeddedName is the identifier an embedded field is declared as, which is what
// names it when the field carries a JSON tag.
func embeddedName(expr ast.Expr) string {
	if ptr, ok := expr.(*ast.StarExpr); ok {
		expr = ptr.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// jsonTag reads the name, whether the field is optional, and whether the field
// is hidden from JSON.
func jsonTag(tag string) (name string, optional, skip bool) {
	const prefix = `json:"`
	at := strings.Index(tag, prefix)
	if at < 0 {
		return "", false, false
	}

	rest := tag[at+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return "", false, false
	}

	parts := strings.Split(rest[:end], ",")
	if parts[0] == "-" {
		return "", false, true
	}
	for _, option := range parts[1:] {
		if option == "omitempty" {
			optional = true
		}
	}
	return parts[0], optional, false
}

// render prints one type expression the way the source wrote it.
func render(fset *token.FileSet, expr ast.Expr) string {
	var buf strings.Builder
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return "?"
	}
	return buf.String()
}

// packageOf splits "pkg.Type" into the package name.
func packageOf(key string) string {
	pkg, _, _ := strings.Cut(key, ".")
	return pkg
}

// typeOf splits "pkg.Type" into the type name.
func typeOf(key string) string {
	_, name, _ := strings.Cut(key, ".")
	return name
}

// Render writes the contract as a TypeScript module. It is a module rather than
// a JSON file on purpose: the main process compiles with tsc, which copies no
// data files, and a generated .ts is one the compiler checks like any other.
func Render(c Contract) []byte {
	var b strings.Builder

	b.WriteString("// Code generated by cmd/flt-contract. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// The Go source is the contract. Regenerate with\n")
	b.WriteString("//   go run ./cmd/flt-contract -out gui/electron/contract.ts\n")
	b.WriteString("// and let `-check` fail a build where this file has drifted.\n\n")

	writeNested(&b, "methods", c.Methods)
	b.WriteString("\n")
	writeNested(&b, "constants", c.Constants)
	b.WriteString("\n")
	writeTypes(&b, c.Types)

	return []byte(b.String())
}

// writeNested renders a flat "pkg.Name" map as one object per package, so a
// caller writes methods.session.MethodAttach rather than a string key.
func writeNested(b *strings.Builder, name string, values map[string]string) {
	b.WriteString("export const " + name + " = {\n")

	pkgs := map[string]map[string]string{}
	for key, value := range values {
		pkg, field, ok := strings.Cut(key, ".")
		if !ok {
			continue
		}
		if pkgs[pkg] == nil {
			pkgs[pkg] = map[string]string{}
		}
		pkgs[pkg][field] = value
	}

	pkgNames := make([]string, 0, len(pkgs))
	for pkg := range pkgs {
		pkgNames = append(pkgNames, pkg)
	}
	sort.Strings(pkgNames)

	for _, pkg := range pkgNames {
		fields := pkgs[pkg]
		fieldNames := make([]string, 0, len(fields))
		for field := range fields {
			fieldNames = append(fieldNames, field)
		}
		sort.Strings(fieldNames)

		b.WriteString("  " + pkg + ": {\n")
		for _, field := range fieldNames {
			b.WriteString("    " + field + ": " + quote(fields[field]) + ",\n")
		}
		b.WriteString("  },\n")
	}

	b.WriteString("} as const;\n")
}

// writeTypes renders the wire shapes as data a test can compare against the
// interfaces the renderer reads. It is not an interface of its own: the shapes
// are read by hand in gui/src, and this is what makes that reading checkable.
func writeTypes(b *strings.Builder, types []Type) {
	b.WriteString("export const wireTypes = {\n")
	for _, typ := range types {
		b.WriteString("  " + quote(typ.Key) + ": {\n")
		for _, field := range typ.Fields {
			b.WriteString("    " + quote(field.Name) + ": { go: " + quote(field.Go) + ", type: " + quote(field.Type))
			if field.Optional {
				b.WriteString(", optional: true")
			}
			b.WriteString(" },\n")
		}
		b.WriteString("  },\n")
	}
	b.WriteString("} as const;\n")
}

// quote renders a Go string as a TypeScript string literal.
func quote(s string) string {
	return strconv.Quote(s)
}

// Check reports whether path already holds the contract for root. It returns the
// bytes the file should hold, so a caller can name the drift.
func Check(root, path string) (want []byte, same bool, err error) {
	c, err := Build(root)
	if err != nil {
		return nil, false, err
	}
	want = Render(c)

	have, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return want, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return want, string(have) == string(want), nil
}

// Write renders the contract for root into w.
func Write(root string, w io.Writer) error {
	c, err := Build(root)
	if err != nil {
		return err
	}
	if _, err := w.Write(Render(c)); err != nil {
		return fmt.Errorf("write the contract: %w", err)
	}
	return nil
}
