// notescheck 检查源码里的讲解是否到位。
//
// 规则只有两条，来自 ADR-0006 与规格 #1：
//
//  1. 每个顶层的 func / type 声明上方必须有契约注释（紧邻的注释组）。
//  2. 函数体内不得出现连续超过 8 行无讲解的代码。
//
// 声明回答「它承诺什么」，函数体回答「它怎么做到」，两者都不能空着。
// 对 Go 以外的源码（建表语句、脚本、前端）只检查第 2 条。
//
// 用法：
//
//	go run ./cmd/notescheck            # 检查当前仓库
//	go run ./cmd/notescheck ./internal # 只检查某个目录
//	go run ./cmd/notescheck -selftest  # 用内置样本验证检查器本身
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxRun 是函数体内允许出现的、连续无讲解代码行的上限。
const maxRun = 8

// violation is one place where the narration is missing: a source position
// plus the sentence saying what is expected there.
type violation struct {
	pos token.Position
	msg string
}

// String renders a violation as file:line: message — the shape editors and
// grep can jump to.
func (v violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.pos.Filename, v.pos.Line, v.msg)
}

// skipDirs 是源码扫描一律跳过的目录：构建产物、依赖、二进制与数据。
var skipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
	"dist": true, "bin": true, "data": true,
}

// main parses flags, then either self-tests or scans the given roots and prints
// every gap it finds. A non-zero exit means "there is work to do".
func main() {
	selftest := flag.Bool("selftest", false, "用内置样本验证检查器本身")
	flag.Parse()

	if *selftest {
		os.Exit(runSelftest())
	}

	// 不传参数时默认扫当前目录，这样 `go run ./cmd/notescheck` 直接就能跑。
	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	// 扫描是纯读：不写文件、不联网，出错只会是「读不到」这类问题，所以直接退出。
	all, err := scan(roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "notescheck:", err)
		os.Exit(2)
	}

	// 输出必须稳定：同一个仓库跑两次要给出同样的行序，否则没法拿两次结果做对比。
	sort.Slice(all, func(i, j int) bool {
		if all[i].pos.Filename != all[j].pos.Filename {
			return all[i].pos.Filename < all[j].pos.Filename
		}
		return all[i].pos.Line < all[j].pos.Line
	})

	// 每条违规一行，file:line 打头，编辑器与 grep 都能直接跳过去。
	for _, v := range all {
		fmt.Println(v)
	}
	if len(all) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\nnotescheck: %d 处讲解缺失。规则见 docs/adr/0006-narration-inside-source.md\n", len(all))
	os.Exit(1)
}

// scan 遍历 roots 下的源码并汇总违规。
func scan(roots []string) ([]violation, error) {
	var out []violation
	for _, root := range roots {
		// 每个 root 单独走一遍；目录级的错误往上抛，不吞。
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			// 只处理认得的扩展名，其余原样跳过——不认识不等于违规。
			vs, err := checkFile(path)
			if err != nil {
				return err
			}
			out = append(out, vs...)
			return nil
		})
		// 一个 root 走失败就整轮失败：宁可报错，也不要给出「只扫了一半」的假结论。
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkFile 按扩展名分派：Go 走语法树，其余走行扫描。
func checkFile(path string) ([]violation, error) {
	ext := strings.ToLower(filepath.Ext(path))
	// 按扩展名分派：Go 有语法树可用，其余语言只能做行扫描。
	switch ext {
	case ".go":
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		// 读不到文件就返回错误，不把「解析失败」当成「没有违规」。
		return checkGo(path, src)
	case ".sql", ".mjs", ".js", ".ts", ".vue":
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		// 拿不到语法树，只看注释密度；这一档不检查声明注释。
		return checkLines(path, src, commentStyle(ext)), nil
	default:
		return nil, nil
	}
}

// style lists the comment markers of one language: the line-comment prefixes
// and, where the language has them, block openers paired with their closers.
type style struct {
	line  []string
	block [][2]string
}

// commentStyle returns the markers to look for in files with this extension.
func commentStyle(ext string) style {
	switch ext {
	case ".sql":
		return style{line: []string{"--"}}
	// Vue 里既有 JS 注释也有模板注释，两种都得认，否则模板会被整段当成代码。
	case ".vue":
		return style{line: []string{"//"}, block: [][2]string{{"/*", "*/"}, {"<!--", "-->"}}}
	default:
		return style{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}}
	}
}

// checkGo 用语法树检查声明注释，用行扫描检查函数体内的讲解密度。
func checkGo(path string, src []byte) ([]violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// 两轮遍历声明：先看「有没有契约」，再看「体内讲没讲」。分开跑，报错更好定位。
	var out []violation

	// 规则一：顶层 func / type 必须有契约注释。
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// Doc 是紧贴声明的那一组注释；中间隔一个空行就不算——这个严格度是刻意的。
			if d.Doc == nil {
				out = append(out, violation{
					pos: fset.Position(d.Pos()),
					msg: fmt.Sprintf("声明 %s 上方缺契约注释", d.Name.Name),
				})
			}
		// 类型可能藏在 var / const 分组里，只有 type 分组才要求注释。
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				// 分组注释也算数：一行注释罩住整组类型是可接受的写法。
				if ts.Doc == nil && d.Doc == nil {
					out = append(out, violation{
						pos: fset.Position(ts.Pos()),
						msg: fmt.Sprintf("类型 %s 上方缺契约注释", ts.Name.Name),
					})
				}
			}
		}
	}

	// 规则二：函数体内不得出现连续超过 maxRun 行无讲解的代码。
	lines := strings.Split(string(src), "\n")
	commented := commentLines(fset, f)
	// 跨行字符串字面量里装的是数据，不是逻辑：SQL 这类长语句不能被算成「没讲解的代码」。
	literal := stringLines(fset, f)
	// 只检查有函数体的声明：接口方法与外部声明没有「体内」可言。
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		// 花括号本身不算代码，所以从它下面一行开始、到上面一行结束。
		bodyFrom := fset.Position(fd.Body.Lbrace).Line + 1
		bodyTo := fset.Position(fd.Body.Rbrace).Line - 1
		out = append(out, density(path, lines, commented, literal, fd.Name.Name, bodyFrom, bodyTo)...)
	}
	return out, nil
}

// density 在 [from, to] 行区间里找连续无讲解的代码段。
func density(path string, lines []string, commented, literal map[int]bool, name string, from, to int) []violation {
	var out []violation

	// run 是当前连续代码行数；reported 让同一段只报一次，否则一段长代码会刷出一屏。
	run := 0
	runFrom := 0
	reported := false

	for ln := from; ln <= to; ln++ {
		// 越界保护：语法树给的行号偶尔会超出切片（空文件、结尾没有换行）。
		if ln < 1 || ln > len(lines) {
			continue
		}
		trimmed := strings.TrimSpace(lines[ln-1])

		if trimmed == "" || closingOnly(trimmed) || literal[ln] {
			continue
		}
		if commented[ln] || isCommentStart(trimmed) {
			run, reported = 0, false
			continue
		}

		// 第一行代码是这一段的起点，报错要指它而不是指此刻的终点：读者要回去补的地方是起点。
		if run == 0 {
			runFrom = ln
		}
		// 走到这里就是一行真代码：计数加一，必要时把它报出来。
		run++
		if run > maxRun && !reported {
			out = append(out, violation{
				pos: token.Position{Filename: path, Line: runFrom},
				msg: fmt.Sprintf("%s 的函数体内有连续 %d 行代码没有讲解（上限 %d，此处到第 %d 行）", name, run, maxRun, ln),
			})
			reported = true
		}
	}
	return out
}

// commentLines 把每个被注释覆盖到的行号收进集合，多行注释按行展开。
func commentLines(fset *token.FileSet, f *ast.File) map[int]bool {
	// 用集合而不是区间：查「某一行有没有被注释覆盖」是这步唯一的需求。
	out := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			start := fset.Position(c.Pos()).Line
			// 多行块注释按换行数展开，否则它罩住的几行会被误判成没人讲。
			count := strings.Count(c.Text, "\n")
			for i := 0; i <= count; i++ {
				out[start+i] = true
			}
		}
	}
	return out
}

// stringLines returns the lines that sit *inside* a multi-line string literal.
// 那些行是数据（SQL 语句、内嵌样本），不是可以讲解的代码，所以既不计数也不重置。
func stringLines(fset *token.FileSet, f *ast.File) map[int]bool {
	out := map[int]bool{}
	// Inspect 会把整棵树走完，所以每个字面量都会被看到，不需要提前剪枝。
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		from := fset.Position(lit.Pos()).Line
		to := fset.Position(lit.End()).Line
		// 只标中间那几行：首行和末行通常还挂着真代码（赋值、逗号、右括号）。
		for ln := from + 1; ln < to; ln++ {
			out[ln] = true
		}
		return true
	})
	return out
}

// checkLines 用于非 Go 源码：只跑讲解密度这一条规则。
func checkLines(path string, src []byte, st style) []violation {
	lines := strings.Split(string(src), "\n")

	// 与 density 同一套计数规则，区别是这里没有语法树，只有文本。
	var out []violation
	// 建表语句里的字段列表等同于 Go 的 struct 字段列表：它是数据的形状，
	// 不是「函数体里的逻辑」，所以既不计数也不重置。
	inCreate := false
	run, runFrom, reported := 0, 0, false
	inBlock := false
	var blockEnd string

	for i, raw := range lines {
		ln := i + 1
		trimmed := strings.TrimSpace(raw)

		// 建表语句块内部：直接跳过，直到这一块收尾。
		if inCreate {
			if strings.HasPrefix(trimmed, ");") {
				inCreate = false
			}
			continue
		}
		// 块注释里的一切都算讲解，直到遇到它的结束标记。
		// 块注释内部：一直跳过，直到这一行里出现它的结束标记。
		if inBlock {
			if strings.Contains(trimmed, blockEnd) {
				inBlock = false
			}
			run, reported = 0, false
			continue
		}
		// 空白与收尾符号跳过：它们既不算代码，也不打断计数。
		if trimmed == "" || closingOnly(trimmed) {
			continue
		}
		// 建表语句的开头。单行写完的不进入块状态（fts5 那张就是一行）。
		if strings.HasPrefix(trimmed, "CREATE TABLE") || strings.HasPrefix(trimmed, "CREATE VIRTUAL TABLE") {
			if !strings.Contains(trimmed, ");") {
				inCreate = true
			}
			continue
		}
		// 单行就能闭合的块注释（/* … */）不会把状态带进下一行。
		if started, end := blockStart(trimmed, st); started {
			if !strings.Contains(trimmed, end) {
				inBlock, blockEnd = true, end
			}
			run, reported = 0, false
			continue
		}
		// 行注释，以及块注释的续行（星号打头），都算讲解。
		if isCommentStart(trimmed) || strings.HasPrefix(trimmed, "*") {
			run, reported = 0, false
			continue
		}
		// import 块对应 Go 的 import 块：它们是声明，不算「函数体里的代码」。
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "import{") {
			run, reported = 0, false
			continue
		}

		// 与 Go 那一路相同：到超限才报，一段只报一次，且指向这一段的第一行。
		if run == 0 {
			runFrom = ln
		}
		// 走到这里就是一行真代码，与 Go 那一路同样的计数方式。
		run++
		if run > maxRun && !reported {
			out = append(out, violation{
				pos: token.Position{Filename: path, Line: runFrom},
				msg: fmt.Sprintf("有连续 %d 行代码没有讲解（上限 %d，此处到第 %d 行）", run, maxRun, ln),
			})
			reported = true
		}
	}
	return out
}

// blockStart 判断这一行是否开启一段块注释，并返回它的结束标记。
func blockStart(trimmed string, st style) (bool, string) {
	for _, b := range st.block {
		if strings.HasPrefix(trimmed, b[0]) {
			return true, b[1]
		}
	}
	return false, ""
}

// isCommentStart 判断一行是否以行注释起始符号开头。
func isCommentStart(trimmed string) bool {
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") ||
		strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "<!--")
}

// closingOnly 判断这一行是不是只有收尾符号：它的存在不影响「连续代码」的计数。
// htmlClose 判断这一行是不是一个 HTML / Vue 的闭合标签，例如 </div>。
// 它和 } 一样不携带逻辑：只是把上面打开的那一层收回去。模板里连着写四五个
// </div> 是正常形状，不该因此被算成「没有讲解的代码」。
func htmlClose(trimmed string) bool {
	if len(trimmed) < 4 || trimmed[0] != '<' || trimmed[1] != '/' || trimmed[len(trimmed)-1] != '>' {
		return false
	}
	for i := 2; i < len(trimmed)-1; i++ {
		c := trimmed[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// closingOnly 判断这一行是否只有收尾符号、没有实际逻辑。这类行不打断讲解的
// 连续性：段与段之间那几个 } 或 </div> 不是没人讲，而是本来就无话可讲。
func closingOnly(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	if htmlClose(trimmed) {
		return true
	}
	// 只要出现一个非收尾字符，这一行就是真代码。
	hasBrace := false
	for _, r := range trimmed {
		switch r {
		case '}', ')', ']':
			hasBrace = true
		case '(', '{', '[', ',', ';', ' ', '\t':
		default:
			return false
		}
	}
	// 至少得有一个收尾符号才算数，纯空白行不算「只有收尾」。
	return hasBrace
}

// runSelftest 用内置样本验证两条规则都会真的报错、且干净样本不误报。
func runSelftest() int {
	const badGo = `package sample

func Bad(name string) string {
	a := 1
	b := 2
	c := 3
	d := 4
	e := 5
	f := 6
	g := 7
	h := 8
	i := 9
	return name
}
`

	const goodGo = `package sample

// Good 返回输入。
func Good(name string) string {
	// 原样返回，不做任何处理。
	return name
}
`

	const badSQL = `a
b
c
d
e
f
g
h
i
`

	// 坏样本必须报错：先证明尺子会响，再去量别人。
	bad, err := checkGo("selftest_bad.go", []byte(badGo))
	if err != nil {
		fmt.Fprintln(os.Stderr, "selftest: 解析坏样本失败:", err)
		return 1
	}
	if len(bad) < 2 {
		fmt.Fprintf(os.Stderr, "selftest: 坏样本只报出 %d 处，期望至少 2 处（缺声明注释 + 体内无讲解）", len(bad))
		fmt.Fprintln(os.Stderr)
		return 1
	}

	// 好样本必须不报错：只会报错的尺子等于尺子坏了。
	good, err := checkGo("selftest_good.go", []byte(goodGo))
	if err != nil {
		fmt.Fprintln(os.Stderr, "selftest: 解析好样本失败:", err)
		return 1
	}
	if len(good) != 0 {
		fmt.Fprintf(os.Stderr, "selftest: 好样本被误报 %d 处: %v", len(good), good)
		fmt.Fprintln(os.Stderr)
		return 1
	}

	// 行扫描那一路也要单独验一次，免得只验了语法树。
	if vs := checkLines("selftest_bad.sql", []byte(badSQL), commentStyle(".sql")); len(vs) == 0 {
		fmt.Fprintln(os.Stderr, "selftest: 行扫描没有报出坏样本")
		return 1
	}

	fmt.Println("notescheck: selftest 通过（坏样本报错、好样本不误报）")
	return 0
}
