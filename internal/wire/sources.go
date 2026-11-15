package wire

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// sources reads the Go source of the packages the types come from: doc
// comments (reflection can't see them) and the constants of an enum's block.
type sources struct {
	fsys   fs.FS
	module string
	pkgs   map[string]*pkgInfo // by import path
}

type pkgInfo struct {
	files []*ast.File
}

type typeInfo struct {
	doc  string
	spec *ast.TypeSpec
}

func (s *sources) pkg(importPath string) (*pkgInfo, error) {
	if p, ok := s.pkgs[importPath]; ok {
		return p, nil
	}
	dir, ok := strings.CutPrefix(importPath, s.module+"/")
	if !ok {
		return nil, fmt.Errorf("wire: package %s is outside the module %s", importPath, s.module)
	}
	entries, err := fs.ReadDir(s.fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("wire: read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	p := &pkgInfo{}
	for _, n := range names {
		src, err := fs.ReadFile(s.fsys, path.Join(dir, n))
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, path.Join(dir, n), src, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("wire: %w", err)
		}
		p.files = append(p.files, f)
	}
	s.pkgs[importPath] = p
	return p, nil
}

// typeDoc finds the declaration of type name in the package.
func (s *sources) typeDoc(importPath, name string) (typeInfo, error) {
	p, err := s.pkg(importPath)
	if err != nil {
		return typeInfo{}, err
	}
	for _, f := range p.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if ts.Name.Name != name {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				return typeInfo{doc: doc.Text(), spec: ts}, nil
			}
		}
	}
	return typeInfo{}, fmt.Errorf("wire: type %s not found in %s", name, importPath)
}

// fieldDoc is the comment of a struct field ("" when it has none, or the
// type is not a named struct).
func (s *sources) fieldDoc(importPath, typeName, field string) (string, error) {
	if typeName == "" || importPath == "" || strings.Contains(typeName, "[") {
		return "", nil
	}
	ti, err := s.typeDoc(importPath, typeName)
	if err != nil {
		return "", err
	}
	st, ok := ti.spec.Type.(*ast.StructType)
	if !ok {
		return "", nil
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name != field {
				continue
			}
			if f.Doc != nil {
				return f.Doc.Text(), nil
			}
			return f.Comment.Text(), nil
		}
	}
	return "", nil
}

// constBlock lists the string values of the const block that declares
// anchor, in declaration order. With typ, every constant must be of that type.
func (s *sources) constBlock(importPath, anchor string, typ reflect.Type) ([]string, error) {
	p, err := s.pkg(importPath)
	if err != nil {
		return nil, err
	}
	for _, f := range p.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST || !declares(gd, anchor) {
				continue
			}
			var out []string
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if n.Name == "_" {
						continue
					}
					if typ != nil {
						if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != typ.Name() {
							return nil, fmt.Errorf("constant %s is not a %s", n.Name, typ.Name())
						}
					}
					if i >= len(vs.Values) {
						return nil, fmt.Errorf("constant %s has no literal value", n.Name)
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return nil, fmt.Errorf("constant %s is not a string literal", n.Name)
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						return nil, err
					}
					out = append(out, v)
				}
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("constant %s not found in %s", anchor, importPath)
}

func declares(gd *ast.GenDecl, name string) bool {
	for _, spec := range gd.Specs {
		for _, n := range spec.(*ast.ValueSpec).Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}
