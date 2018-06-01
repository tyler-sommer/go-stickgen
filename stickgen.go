package stickgen // import "github.com/veonik/go-stickgen"

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"regexp"
	"strings"

	"github.com/tyler-sommer/stick"
	"github.com/tyler-sommer/stick/parse"
)

var notWord = regexp.MustCompile(`[^\w]|[_]`)

func titleize(in string) string {
	return strings.Replace(strings.Title(notWord.ReplaceAllString(in, " ")), " ", "", -1)
}

type renderer func()

type evaluatedExpr struct {
	body          string
	isFunction    bool
	hasError      bool
	resultantName string
}

// A Generator handles generating Go code from Twig templates.
type Generator struct {
	pkgName string
	loader  stick.Loader
	out     *bytes.Buffer
	name    string
	imports map[string]bool
	blocks  map[string]renderer
	args    map[string]bool
	root    bool
	stack   []string
	tabs    int
}

// Generate parses the given template and outputs the generated code.
func (g *Generator) Generate(name string) (string, error) {
	err := g.generate(name)
	if err != nil {
		return "", err
	}
	return g.output(), nil
}

// NewGenerator creates a new code generator using the given Loader.
func NewGenerator(pkgName string, loader stick.Loader) *Generator {
	g := &Generator{
		pkgName: pkgName,
		loader:  loader,
		name:    "",
		out:     &bytes.Buffer{},
		imports: map[string]bool{
			"github.com/tyler-sommer/stick": true,
			"io": true,
		},
		blocks: make(map[string]renderer),
		args:   make(map[string]bool),
		root:   true,
		stack:  make([]string, 0),
		tabs:   1,
	}

	return g
}

func (g *Generator) indent() string {
	return strings.Repeat("	", g.tabs)
}

func (g *Generator) generate(name string) error {
	tpl, err := g.loader.Load(name)
	if err != nil {
		return err
	}

	body, err := ioutil.ReadAll(tpl.Contents())
	if err != nil {
		return err
	}
	tree, err := parse.Parse(string(body))
	if err != nil {
		return err
	}
	g.name = name
	g.stack = append(g.stack, name)
	g.root = len(g.stack) == 1
	if !g.root {
		defer func() {
			g.stack = g.stack[:len(g.stack)-1]
			g.name = g.stack[len(g.stack)-1]
			g.root = len(g.stack) == 1
		}()
	}
	g.walk(tree.Root())
	return nil
}

func (g *Generator) output() string {
	body := g.out.String()
	funcs := make([]string, 0)
	for _, block := range g.blocks {
		g.out.Reset()
		block()
		funcs = append(funcs, g.out.String())
	}
	imports := make([]string, 0)
	for v, _ := range g.imports {
		imports = append(imports, fmt.Sprintf(`"%s"`, v))
	}

	return fmt.Sprintf(`// Code generated by stickgen.
// DO NOT EDIT!

package %s

import (
	%s
)

%s

func Template%s(env *stick.Env, output io.Writer, ctx map[string]stick.Value) {
%s}
`, g.pkgName, strings.Join(imports, "\n	"), strings.Join(funcs, "\n"), titleize(g.name), body)
}

func (g *Generator) addImport(name string) {
	if _, ok := g.imports[name]; !ok {
		g.imports[name] = true
	}
}

func (g *Generator) walk(n parse.Node) error {
	switch node := n.(type) {
	case *parse.ModuleNode:
		if node.Parent != nil {
			if name, ok := g.evaluate(node.Parent.Tpl); ok {
				err := g.generate(name)
				if err != nil {
					return err
				}
			} else {
				// TODO: Handle more than just string literals
				return errors.New("Unable to evaluate extends reference")
			}
		}
		return g.walk(node.BodyNode)
	case *parse.BodyNode:
		for _, child := range node.All() {
			err := g.walk(child)
			if err != nil {
				return err
			}
		}
	case *parse.IncludeNode:
		if name, ok := g.evaluate(node.Tpl); ok {
			err := g.generate(name)
			if err != nil {
				return err
			}
		} else {
			// TODO: Handle more than just string literals
			return errors.New("Unable to evaluate include reference")
		}
	case *parse.TextNode:
		g.addImport("fmt")
		g.out.WriteString(fmt.Sprintf(`%s// line %d, offset %d in %s
%sfmt.Fprint(output, %s)
`, g.indent(), node.Line, node.Offset, g.name, g.indent(), fmt.Sprintf("`%s`", node.Data)))
	case *parse.PrintNode:
		v, err := g.walkExpr(node.X)
		if err != nil {
			return err
		}
		g.addImport("fmt")
		g.out.WriteString(fmt.Sprintf(`%s// line %d, offset %d in %s
`, g.indent(), node.Line, node.Offset, g.name))
		if v.isFunction {
			// TODO: The goggles, they do nothing!
			g.out.WriteString(fmt.Sprintf(`%s{
`, g.indent()))
			g.tabs++
			g.out.WriteString(fmt.Sprintf(`%s%s
`, g.indent(), v.body))
			if v.hasError {
				g.out.WriteString(fmt.Sprintf(`%sif err == nil {
`, g.indent()))
				g.tabs++
				g.out.WriteString(fmt.Sprintf(`%sfmt.Fprint(output, %s)
`, g.indent(), v.resultantName))
				g.tabs--
				g.out.WriteString(fmt.Sprintf(`%s}
`, g.indent()))
			} else {
				g.out.WriteString(fmt.Sprintf(`%sfmt.Fprint(output, %s)
`, g.indent(), v.resultantName))
			}
			g.tabs--
			g.out.WriteString(fmt.Sprintf(`%s}
`, g.indent()))
		} else {
			g.out.WriteString(fmt.Sprintf(`%sfmt.Fprint(output, %s)
`, g.indent(), v.resultantName))
		}

	case *parse.BlockNode:
		g.addImport("fmt")
		g.blocks[node.Name] = func(g *Generator, node *parse.BlockNode, rootName string) renderer {
			// TODO: Wow, I don't know about all this.
			return func() {
				g.out.WriteString(fmt.Sprintf(`func block%s%s(env *stick.Env, output io.Writer, ctx map[string]stick.Value) {
`, titleize(rootName), titleize(node.Name)))
				g.walk(node.Body)
				g.out.WriteString(`}`)
			}
		}(g, node, g.stack[0])
		if !g.root {
			g.out.WriteString(fmt.Sprintf(`%s// line %d, offset %d in %s
%sblock%s%s(env, output, ctx)
`, g.indent(), node.Line, node.Offset, g.name, g.indent(), titleize(g.stack[0]), titleize(node.Name)))
		}
	case *parse.ForNode:
		name, err := g.walkExpr(node.X)
		if err != nil {
			return err
		}
		key := "_"
		if node.Key != "" {
			key = node.Key
			g.args[key] = true
		}
		val := node.Val
		g.args[val] = true
		g.out.WriteString(fmt.Sprintf(`%s// line %d, offset %d in %s
%sstick.Iterate(%s, func(%s, %s stick.Value, loop stick.Loop) (brk bool, err error) {
`, g.indent(), node.Line, node.Offset, g.name, g.indent(), name.resultantName, key, val))
		g.tabs++
		g.walk(node.Body)
		delete(g.args, val)
		delete(g.args, key)
		g.out.WriteString(fmt.Sprintf(`%sreturn false, nil
`, g.indent()))
		g.tabs--
		g.out.WriteString(fmt.Sprintf(`%s})
`, g.indent()))
	case *parse.IfNode:
		cond, err := g.walkExpr(node.Cond)
		if err != nil {
			return err
		}
		g.out.WriteString(fmt.Sprintf(`%s// line %d, offset %d in %s
`, g.indent(), node.Line, node.Offset, g.name))
		var errCheck string = ""
		if cond.isFunction {
			g.out.WriteString(fmt.Sprintf(`%s{
%s	%s
`, g.indent(), g.indent(), cond.body))
			g.tabs++
			defer func() {
				g.tabs--
				g.out.WriteString(fmt.Sprintf(`%s}
`, g.indent()))
			}()
			if cond.hasError {
				errCheck = "err == nil && "
			}
		}
		g.out.WriteString(fmt.Sprintf(`%sif %sstick.CoerceBool(%s) {
`, g.indent(), errCheck, cond.resultantName))
		g.tabs++
		g.walk(node.Body)
		g.tabs--
		if len(node.Else.All()) > 0 {
			g.out.WriteString(fmt.Sprintf(`%s} else {
`, g.indent()))
			g.tabs++
			g.walk(node.Else)
			g.tabs--
		}
		g.out.WriteString(fmt.Sprintf(`%s}
`, g.indent()))
	}
	return nil
}

func (g *Generator) evaluate(e parse.Expr) (string, bool) {
	switch expr := e.(type) {
	case *parse.StringExpr:
		return expr.Text, true
	}
	return "", false
}

func newNameExpr(name string) evaluatedExpr {
	return evaluatedExpr{body: name, resultantName: name, isFunction: false, hasError: false}
}

var emptyExpr = evaluatedExpr{body: "", resultantName: "", isFunction: false, hasError: false}

func (g *Generator) walkExpr(e parse.Expr) (evaluatedExpr, error) {
	switch expr := e.(type) {
	case *parse.NameExpr:
		if _, ok := g.args[expr.Name]; ok {
			return newNameExpr(expr.Name), nil
		}
		return newNameExpr("ctx[\"" + expr.Name + "\"]"), nil
	case *parse.StringExpr:
		return newNameExpr(`"` + expr.Text + `"`), nil
	case *parse.GetAttrExpr:
		if len(expr.Args) > 0 {
			return emptyExpr, errors.New("Method calls are currently unsupported.")
		}
		attr, err := g.walkExpr(expr.Attr)
		if err != nil {
			return emptyExpr, err
		}
		name, err := g.walkExpr(expr.Cont)
		if err != nil {
			return emptyExpr, err
		}
		return evaluatedExpr{body: `val, err := stick.GetAttr(` + name.resultantName + `, ` + attr.resultantName + `)`, resultantName: "val", isFunction: true, hasError: true}, nil
	case *parse.FuncExpr:
		if len(expr.Args) != 1 {
			return emptyExpr, errors.New("Function currently calls only support a single argument.")
		}
		arg, err := g.walkExpr(expr.Args[0])
		if err != nil {
			return emptyExpr, err
		}
		var argBody string = ""
		if arg.isFunction {
			// TODO: Handle error
			argBody = strings.Replace(arg.body, "err", "_", 1)
		}
		// TODO: nil stick.Context is passed into the function!
		return evaluatedExpr{
			body: fmt.Sprintf(`%s
%s	var fnval stick.Value = ""
%s	if fn, ok := env.Functions["%s"]; ok {
%s		fnval = fn(nil, %s)
%s	}`, argBody, g.indent(), g.indent(), expr.Name, g.indent(), arg.resultantName, g.indent()),
			resultantName: "fnval",
			isFunction:    true,
			hasError:      false,
		}, nil
	case *parse.BinaryExpr:
		switch expr.Op {
		case parse.OpBinaryEqual:
			var pre string = ""
			left, err := g.walkExpr(expr.Left)
			if err != nil {
				return emptyExpr, err
			}
			right, err := g.walkExpr(expr.Right)
			if err != nil {
				return emptyExpr, err
			}
			if left.isFunction {
				// TODO: Handle error
				pre = pre + strings.Replace(left.body, "err", "_ ", 1)
			}
			if right.isFunction {
				if pre != "" {
					pre = pre + "\n" + g.indent()
				}
				// TODO: Handle error
				pre = pre + strings.Replace(strings.Replace(left.body, "err", "_ ", 1), right.resultantName, "right", 1)
				right.resultantName = "right"
			}
			res := evaluatedExpr{
				body:          pre,
				isFunction:    left.isFunction || right.isFunction,
				hasError:      false,
				resultantName: fmt.Sprintf(`stick.Equal(%s, %s)`, left.resultantName, right.resultantName),
			}
			return res, nil

		default:
			return emptyExpr, fmt.Errorf("stickgen: unsupported binary operator: %s", expr.Op)
		}
	}
	return emptyExpr, nil
}
