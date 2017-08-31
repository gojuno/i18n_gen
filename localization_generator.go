package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func GetLocalizationJsonFromSources(path string) string {
	start := time.Now()
	v = NewFuncVisit()
	err := filepath.Walk(path, findLocalizedStrings)
	if err != nil {
		log.Fatal(err)
	}
	v.wg.Wait()
	jsonData := v.MakeJson()
	log.Println("Localized data was genereated for", time.Since(start))
	return jsonData
}

type FuncVisitor struct {
	sync.Mutex
	wg        sync.WaitGroup
	funcNames map[string]struct{}
}

var v *FuncVisitor

func NewFuncVisit() *FuncVisitor {
	v := new(FuncVisitor)
	v.funcNames = make(map[string]struct{})
	return v
}

func (v *FuncVisitor) Add(id string) {
	v.Lock()
	defer v.Unlock()
	v.funcNames[id] = struct{}{}
}

func (v *FuncVisitor) Visit(node ast.Node) (w ast.Visitor) {
	if fCall, ok := node.(*ast.CallExpr); ok {
		fs, ok := fCall.Fun.(*ast.SelectorExpr) //some package's function call
		if ok {
			switch fs.Sel.Name {
			case "NewI18nString":
				arg0 := fCall.Args[0]
				switch expr := arg0.(type) {
				case *ast.BasicLit:
					if expr.Kind.String() != "STRING" {
						log.Fatalf("In call NewI18nString(id) id should be string literal! Got:%#v", expr)
					}
					v.Add(expr.Value[1 : len(expr.Value)-1])
				default:
					log.Fatalf("In call NewI18nString(id) id should be string literal! Got:%#v", expr)
				}
			}
		}
	}
	return v
}

func (v *FuncVisitor) MakeJson() string {
	storage := []map[string]string{}
	for v, _ := range v.funcNames {
		m := map[string]string{}
		m["id"] = v
		m["translation"] = v
		storage = append(storage, m)
	}

	s, err := json.MarshalIndent(storage, "", "  ")
	if err != nil {
		log.Fatal(err)
	}

	return string(s)
}

func findLocalizedStrings(path string, info os.FileInfo, err error) error {
	if err != nil {
		log.Print(err)
		return nil
	}
	if strings.HasSuffix(path, "api/i18n.go") {
		go func() {
			v.wg.Add(1)
			defer v.wg.Done()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				log.Print(err)
			}
			ast.Walk(v, file)
		}()
	}
	return nil
}
