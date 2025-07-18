package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"sync"
	"unicode/utf8"

	"github.com/carn181/faustlsp/logging"
	"github.com/carn181/faustlsp/parser"
	"github.com/carn181/faustlsp/transport"
	"github.com/carn181/faustlsp/util"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type File struct {
	URI      util.Uri
	Path     util.Path
	RelPath  util.Path // Path relative to a workspace
	TempPath util.Path // Path for temporary
	Content  []byte
	Open     bool
	Tree     *tree_sitter.Tree
	// To avoid freeing null tree in C
	treeCreated     bool
	hasSyntaxErrors bool
}

func (f *File) LogValue() slog.Value {
	// Create a map with all file attributes
	fileAttrs := map[string]any{
		"URI":      f.URI,
		"Path":     f.Path,
		"RelPath":  f.RelPath,
		"TempPath": f.TempPath,
	}
	return slog.AnyValue(fileAttrs)
}

func (f *File) DocumentSymbols() []transport.DocumentSymbol {
	// TODO: Find a way to have tree without having to worry about
	t := parser.ParseTree(f.Content)
	//	defer t.Close()
	return parser.DocumentSymbols(t, f.Content)
	//	return []transport.DocumentSymbol{}
}

func (f *File) TSDiagnostics() transport.PublishDiagnosticsParams {
	t := parser.ParseTree(f.Content)
	//	defer t.Close()
	errors := parser.TSDiagnostics(f.Content, t)
	if len(errors) == 0 {
		f.hasSyntaxErrors = false
	} else {
		f.hasSyntaxErrors = true
	}
	d := transport.PublishDiagnosticsParams{
		URI:         transport.DocumentURI(f.URI),
		Diagnostics: errors,
	}
	return d
}

type Files struct {
	// Absolute Paths Only
	fs       map[util.Path]*File
	mu       sync.Mutex
	encoding transport.PositionEncodingKind // Position Encoding for applying incremental changes. UTF-16 and UTF-32 supported
}

func (files *Files) Init(context context.Context, encoding transport.PositionEncodingKind) {
	files.fs = make(map[string]*File)
	files.encoding = encoding
}

func (files *Files) OpenFromURI(uri util.Uri, root util.Path, editorOpen bool, temp util.Path) {
	path, err := util.Uri2path(uri)
	if err != nil {
		logging.Logger.Error("OpenFromURI error", "error", err)
		return
	}
	files.OpenFromPath(path, root, editorOpen, uri, temp)
}

func (files *Files) OpenFromPath(path util.Path, root util.Path, editorOpen bool, uri util.Uri, temp util.Path) {
	var file File

	var relPath util.Path
	_, ok := files.Get(path)
	// If File already in store, ignore
	if ok {
		logging.Logger.Info("File already in store", "path", path)
		return
	}

	if root == "" {
		relPath = ""
	} else {
		size := len(root)
		// +1 for / delimeter for only relative path
		relPath = path[size+1:]
	}
	logging.Logger.Info("Reading contents of file", "path", path)

	content, err := os.ReadFile(path)

	if err != nil {
		if os.IsNotExist(err) {
			logging.Logger.Error("Invalid Path", "error", err)
		}
	}

	// Parse Tree
	var tree *tree_sitter.Tree
	var treemade bool
	ext := filepath.Ext(path)
	if ext == ".dsp" || ext == ".lib" {
		//		logging.Logger.Printf("Trying to parse %s\n", content)
		//		tree = parser.ParseTree(content)
		//		treemade = true
	} else {
		//		treemade = false
	}

	if uri == "" {
		uri = util.Path2URI(path)
	}
	file = File{
		Path:        path,
		Content:     content,
		Open:        editorOpen,
		RelPath:     relPath,
		Tree:        tree,
		TempPath:    temp,
		treeCreated: treemade,
		URI:         uri,
	}

	files.mu.Lock()
	files.fs[path] = &file
	files.mu.Unlock()
}

func (files *Files) Get(path util.Path) (*File, bool) {
	files.mu.Lock()
	file, ok := files.fs[path]
	files.mu.Unlock()
	return file, ok
}

func (files *Files) TSDiagnostics(path util.Path) transport.PublishDiagnosticsParams {
	d := transport.PublishDiagnosticsParams{}
	files.mu.Lock()
	file, ok := files.fs[path]
	if ok {
		d = file.TSDiagnostics()

	}
	files.mu.Unlock()
	return d
}

func (files *Files) ModifyFull(path util.Path, content string) {
	files.mu.Lock()
	f, ok := files.fs[path]
	if !ok {
		logging.Logger.Error("file to modify not in file store", "path", path)
		files.mu.Unlock()
		return
	}

	f.Content = []byte(content)

	ext := filepath.Ext(path)
	if ext == ".dsp" || ext == ".lib" {
		if f.treeCreated {
			//			f.Tree.Close()
		}
		//		logging.Logger.Info("Trying to parse file", "content", f.Content)
		//		f.Tree = parser.ParseTree(f.Content)
		//		f.treeCreated = true
	}
	files.mu.Unlock()
}

func (files *Files) ModifyIncremental(path util.Path, changeRange transport.Range, content string) {
	logging.Logger.Info("Applying Incremental Change", "path", path)
	files.mu.Lock()
	f, ok := files.fs[path]
	if !ok {
		logging.Logger.Error("file to modify not in file store", "path", path)
		files.mu.Unlock()
		return
	}
	result := ApplyIncrementalChange(changeRange, content, string(f.Content), string(files.encoding))
	//	logging.Logger.Info("Before/After Incremental Change", "before", string(f.Content), "after", result)
	logging.Logger.Info("Incremental Change Parameters ", "range", changeRange, "content", content)
	logging.Logger.Info("Before/After Incremental Change", "before", string(f.Content), "after", result)
	f.Content = []byte(result)

	ext := filepath.Ext(path)
	if ext == ".dsp" || ext == ".lib" {
		if f.treeCreated {
			//			f.Tree.Close()
		}
		//		logging.Logger.Info("Trying to parse file", "content", f.Content)
		//		f.Tree = parser.ParseTree(f.Content)
		//		f.treeCreated = true
	}
	files.mu.Unlock()
}

// TODO: Maybe have the 3 following functions in util instead of here
func ApplyIncrementalChange(r transport.Range, newContent string, content string, encoding string) string {
	start, _ := PositionToOffset(r.Start, content, encoding)
	end, _ := PositionToOffset(r.End, content, encoding)
	//	logging.Logger.Printf("Start: %d, End: %d\n", start, end)
	return content[:start] + newContent + content[end:]
}

func PositionToOffset(pos transport.Position, s string, encoding string) (uint, error) {
	if len(s) == 0 {
		return 0, nil
	}
	indices := GetLineIndices(s)
	if pos.Line > uint32(len(indices)) {
		return 0, fmt.Errorf("invalid Line Number")
	} else if pos.Line == uint32(len(indices)) {
		return uint(len(s)), nil
	}
	currChar := indices[pos.Line]
	for i := 0; i < int(pos.Character); i++ {
		if int(currChar) >= len(s) {
			break // Prevent reading past end of string
		}
		r, w := utf8.DecodeRuneInString(s[currChar:])
		if w == 0 {
			break // Prevent infinite loop if decoding fails
		}
		currChar += uint(w)
		if encoding == "utf-16" {
			if r >= 0x10000 {
				i++
				if i == int(pos.Character) {
					break
				}
			}
		}
	}
	return currChar, nil
}

func OffsetToPosition(offset uint, s string, encoding string) (transport.Position, error) {
	if len(s) == 0 || offset == 0 {
		return transport.Position{Line: 0, Character: 0}, nil
	}
	line := uint32(0)
	char := uint32(0)
	str := []byte(s)

	for i := uint(0); i < offset && i < uint(len(str)); {
		r, w := utf8.DecodeRune(str[i:])
		if w == 0 {
			break // Prevent infinite loop if decoding fails
		}
		if r == '\n' {
			line++
			char = 0
		} else {
			char++
			if r >= 0x10000 && encoding == "utf-16" {
				char++
			}
		}
		i += uint(w)
	}

	return transport.Position{Line: line, Character: char}, nil
}

func GetLineIndices(s string) []uint {
	//	logging.Logger.Printf("Got %s\n", s)
	lines := []uint{0}
	i := 0
	for w := 0; i < len(s); i += w {
		runeValue, width := utf8.DecodeRuneInString(s[i:])
		if runeValue == '\n' {
			lines = append(lines, uint(i)+1)
		}
		w = width
	}
	return lines
}

func getDocumentEndOffset(s string, encoding string) uint {
	switch encoding {
	case "utf-8":
		return uint(len(s))
	case "utf-16":
		offset := uint(0)
		for _, r := range s {
			if r >= 0x10000 {
				offset += 2
			} else {
				offset += 1
			}
		}
		return offset
	case "utf-32":
		// Each rune is one code unit in utf-32
		return uint(len([]rune(s)))
	default:
		// Fallback to utf-8
		return uint(len(s))
	}
}

func getDocumentEndPosition(s string, encoding string) (transport.Position, error) {
	offset := getDocumentEndOffset(s, encoding)
	pos, err := OffsetToPosition(offset, s, encoding)
	return pos, err
}

func (files *Files) CloseFromURI(uri util.Uri) {
	path, err := util.Uri2path(uri)
	if err != nil {
		logging.Logger.Error("CloseFromURI error", "error", err)
		return
	}
	files.Close(path)
}

func (files *Files) Close(path util.Path) {
	files.mu.Lock()
	f, ok := files.fs[path]
	if !ok {
		logging.Logger.Error("file to close not in file store", "path", path)
		files.mu.Unlock()
		return
	}
	f.Open = false
	files.mu.Unlock()
}

func (files *Files) Remove(path util.Path) {
	files.mu.Lock()
	// TODO: Have a close function for File
	f, ok := files.fs[path]
	if ok {
		ext := filepath.Ext(path)
		if ext == ".dsp" || ext == ".lib" {
			if f.treeCreated {
				//				f.Tree.Close()
			}
		}
	}
	delete(files.fs, path)
	files.mu.Unlock()
}

func (files *Files) String() string {
	str := ""
	for path := range files.fs {
		if IsFaustFile(path) {
			str += fmt.Sprintf("Files has %s\n", path)
		}
	}
	return str
}
