package website

import (
	"embed"
	"fmt"
	"html/template"
	"io"
)

//go:embed all:templates
var templateFiles embed.FS

type TemplateExecutor interface {
	ExecuteTemplate(writer io.Writer, name string, data any) error
}

type DebugTemplateExecutor struct {
	Glob string
}

func (executor DebugTemplateExecutor) ExecuteTemplate(writer io.Writer, name string, data any) error {
	templates, err := template.ParseGlob(executor.Glob)
	if err != nil {
		return fmt.Errorf("parse glob: %w", err)
	}
	return templates.ExecuteTemplate(writer, name, data)
}

func NewTemplateExecutor(reload bool) TemplateExecutor {
	if reload {
		return DebugTemplateExecutor{ "src/website/templates/*.gohtml" }
	} else {
		return template.Must(template.ParseFS(templateFiles, "**/*.gohtml"))
	}
}
