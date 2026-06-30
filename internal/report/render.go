package report

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
)

//go:embed template.html
var templateHTML string

var tmpl = template.Must(template.New("report").Parse(templateHTML))

// Render writes the self-contained dashboard HTML, embedding the Model as JSON
// that the page's client-side JS consumes.
func Render(w io.Writer, m *Model) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, map[string]any{"Data": template.JS(data)})
}
