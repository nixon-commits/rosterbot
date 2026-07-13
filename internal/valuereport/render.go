package valuereport

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
)

//go:embed template.html
var templateHTML string

var tmpl = template.Must(template.New("valuereport").Parse(templateHTML))

// Render writes the self-contained value.html, embedding the Model as JSON that
// the page's client-side JS consumes to draw the multi-team time plot.
func Render(w io.Writer, m *Model) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, map[string]any{"Data": template.JS(data)})
}
