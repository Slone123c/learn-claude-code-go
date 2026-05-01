package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Skill struct {
	Meta map[string]any
	Body string
}

type SkillLoader struct {
	Skills map[string]Skill
}

func NewSkillLoader(skillsDir string) (*SkillLoader, error) {
	loader := &SkillLoader{
		Skills: make(map[string]Skill),
	}

	err := filepath.Walk(skillsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == "SKILL.md" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			meta, body := parseFrontmatter(string(data))

			name, _ := meta["name"].(string)
			if name == "" {
				name = filepath.Base(filepath.Dir(path))
			}
			loader.Skills[name] = Skill{
				Meta: meta,
				Body: body,
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return loader, nil
}

func (l *SkillLoader) GetDescriptions() string {
	var names []string
	for name := range l.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		desc, _ := l.Skills[name].Meta["description"].(string)
		lines = append(lines, fmt.Sprintf("  - %s: %s", name, desc))
	}
	return strings.Join(lines, "\n")

}

func (l *SkillLoader) GetContent(name string) string {
	skill, ok := l.Skills[name]
	if !ok {
		return fmt.Sprintf("Error: Unknown skill '%s'.", name)
	}
	return fmt.Sprintf("<skill name=%q>\n%s\n</skill>", name, skill.Body)
}

func parseFrontmatter(text string) (map[string]any, string) {
	match := regexp.MustCompile(`(?s)^---\n(.*?)\n---\n(.*)$`).FindStringSubmatch(text)
	if match == nil {
		return map[string]any{}, text
	}

	var meta map[string]any
	if err := yaml.Unmarshal([]byte(match[1]), &meta); err != nil || meta == nil {
		meta = map[string]any{}
	}

	body := strings.TrimSpace(match[2])
	return meta, body
}
