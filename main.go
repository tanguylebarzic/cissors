package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/ledongthuc/pdf"
	"gopkg.in/alecthomas/kingpin.v2"
	"gopkg.in/yaml.v3"
)

var (
	verbose  = kingpin.Flag("verbose", "Verbose mode.").Short('v').Bool()
	inFile   = kingpin.Arg("file", "File to parse.").Required().String()
	outFile  = kingpin.Flag("out", "Output to file.").Short('o').String()
	idPrefix = kingpin.Flag("id-prefix", "ID prefix for rules.").String()
)

// Location describes location of the Rule in a CIS benchmark
type Location struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

// Rule describes a CIS benchmark rule
type Rule struct {
	ID       string            `yaml:"id"`
	Name     string            `yaml:"name"`
	Location []Location        `yaml:"location,omitempty"`
	Sections map[string]string `yaml:"-,inline"`
}

var (
	pageMarkerRegex = regexp.MustCompile(`^([\d]+ \| Page)  `)

	titleExtractRegex = regexp.MustCompile(`((\d+\.)*?(\d+))\s([A-Za-z]*)(\s[A-Za-z\:\.,_\-\/\(\)]*?|\s\d{1,3}|\s(?:[0-9]{1,3}\.){3}[0-9]{1,3})*(\.+)?\s\d+\s`)
	titleCropRegex    = regexp.MustCompile(`\s?\.+\s\d+\s$`)
	titleIDRegex      = regexp.MustCompile(`((\d+\.)*?(\d+))\s`)

	sectionRegex = regexp.MustCompile(
		`((Profile Applicability|Description|Rationale|Audit|Remediation|Impact|Default Value|References|CIS Controls)\:\s)`,
	)

	ruleTitleExtractRegex = regexp.MustCompile(`((\d+\.)*?(\d+))\s([A-Za-z]*)(\s[A-Za-z\:\.,_\-\/\(\)]*?|\d{1,3}|(?:[0-9]{1,3}\.){3}[0-9]{1,3})*\((Not\s)?Scored\)`)

	ruleTitleTestRegex = regexp.MustCompile(`\((Not\s)?Scored\)$`)

	nonASCIIRegex = regexp.MustCompile(`[[:^ascii:]]`)
)

func main() {
	kingpin.Parse()
	_, reader, err := pdf.Open(*inFile)
	if err != nil {
		fmt.Fprintf(os.Stdout, "Failed to parse PDF: %v", err)
	}

	fmt.Print("✂️  You are now running with CISsors\n\n")
	fmt.Print("✂️  Skillfully cutting your CIS benchmark️\n\n")
	startPage := 0

	ruleIDToName := map[string]string{}
	ruleCount := 0

	walkPages(reader, 2, reader.NumPage(), func(page int, content string) bool {
		if *verbose {
			fmt.Printf("✂️  Looking for titles in page %d\n", page)
		}

		titles := titleExtractRegex.FindAllString(content, -1)

		if len(ruleIDToName) != 0 && len(titles) == 0 {
			// we will start scanning for rules from this page onward
			startPage = page
			return false
		}

		for _, title := range titles {
			// Crop the trailing part
			title = titleCropRegex.ReplaceAllString(title, "")

			id, name, err := splitTitle(title)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}

			if *verbose {
				fmt.Printf("id: %s - name: %s\n", id, name)
			}
			ruleIDToName[id] = name
			if ruleTitleTestRegex.MatchString(name) {
				ruleCount++
			}
		}
		return true
	})

	fmt.Printf("✂️  Found %d rules\n\n", ruleCount)

	var (
		nextRuleContent string
		nextRuleTitle   string
		rules           []*Rule
	)

	walkPages(reader, startPage, reader.NumPage(), func(page int, content string) bool {
		if *verbose {
			fmt.Printf("✂️  Looking for rules in page %d\n", page)
		}

		r := ruleTitleExtractRegex.FindStringSubmatchIndex(content)

		if len(r) == 0 {
			if nextRuleTitle != "" {
				// Collect the next rule content
				nextRuleContent += "\n" + content
			}
			return true
		}

		if nextRuleTitle != "" {
			// Extract and append the rule
			if *verbose {
				fmt.Printf("-- Rule content for %s --\n", nextRuleTitle)
				fmt.Println(nextRuleContent)
				fmt.Printf("-- End rule content for %s --\n", nextRuleTitle)
			}

			if rule, err := extractRule(nextRuleTitle, nextRuleContent); err == nil {
				rule.Location = getRuleLocation(ruleIDToName, rule.ID)
				rules = append(rules, rule)
				ruleCount--
				if ruleCount == 0 {
					fmt.Printf("✂️  Done extracting rules\n\n")
					return false
				}
			} else {
				fmt.Fprintf(os.Stderr, "Failed to extract rule: %v\n", err)
			}
		}

		nextRuleTitle = content[r[0]:r[1]]
		nextRuleContent = content[r[1]:]
		return true
	})

	yamlData, err := yaml.Marshal(&rules)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to serialize as YAML: %v\n", err)
		return
	}

	fmt.Print("✂️️️  All done! Enjoy your YAML masterpiece!\n\n")

	f := os.Stdout
	if *outFile != "" {
		file, err := os.Create(*outFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open file %v", err)
			return
		}
		defer file.Close()
		f = file
	}

	fmt.Fprintf(f, "---\n%s", string(yamlData))
}

func extractRule(title, content string) (*Rule, error) {
	id, name, err := splitTitle(title)
	if err != nil {
		return nil, fmt.Errorf("Malformed rule title %s: %w", title, err)
	}

	// Extract rule sections after the title
	sections := findNamedValuesByRegex(content, sectionRegex)
	if len(sections) == 0 {
		return nil, fmt.Errorf("No valid sections for rule %s", title)
	}

	rule := &Rule{
		ID:       *idPrefix + id,
		Name:     name,
		Sections: map[string]string{},
	}

	for _, section := range sections {
		rule.Sections[sectionKeyName(section.name)] = sectionContent(section.value)
	}
	return rule, nil
}

func walkPages(reader *pdf.Reader, start, end int, pageFn func(page int, s string) bool) {
	for i := start; i < end; i++ {
		p := reader.Page(i)

		content, err := p.GetPlainText(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to extract page %d as plain text: %v", i, err)
			continue
		}

		content, ok := cutPageMarker(content)
		if !ok {
			fmt.Fprintf(os.Stderr, "Failed to extract page marker on page %d", i)
			continue
		}

		if !pageFn(i, content) {
			break
		}
	}
}

func cutPageMarker(s string) (string, bool) {
	// Chop the page marker
	pm := pageMarkerRegex.FindStringSubmatchIndex(s)
	if len(pm) == 0 {
		return "", false
	}
	return s[pm[1]:], true
}

func splitTitle(title string) (id, name string, err error) {
	// Split into bullet index and actual name
	idxID := titleIDRegex.FindStringSubmatchIndex(title)
	if len(idxID) == 0 {
		err = fmt.Errorf("failed to split title into id and name: %s", title)
		return
	}

	id = title[idxID[0] : idxID[1]-1]
	name = title[idxID[1]:]
	return
}

type namedValue struct {
	name  string
	value string
}

func findNamedValuesByRegex(s string, r *regexp.Regexp) []namedValue {
	hits := r.FindAllStringSubmatchIndex(s, -1)
	var result []namedValue

	for h := 0; h < len(hits); h++ {
		hit := hits[h]
		name := s[hit[0]:hit[1]]
		var value string
		if h != len(hits)-1 {
			value = s[hit[1]:hits[h+1][0]]
		} else {
			value = s[hit[1]:]
		}

		result = append(result, namedValue{
			name:  name,
			value: value,
		})

	}
	return result
}

func sectionKeyName(name string) string {
	key := strings.ToLower(strings.Trim(name, " :"))
	return strings.ReplaceAll(key, " ", "_")
}

func sectionContent(content string) string {
	content = strings.TrimSpace(nonASCIIRegex.ReplaceAllLiteralString(content, ""))
	return strings.ReplaceAll(content, "  ", " ")
}

func getRuleLocation(ruleIDToName map[string]string, ruleID string) []Location {
	var loc []Location
	const sep = "."
	parts := strings.Split(ruleID, sep)
	for i := 0; i < len(parts)-1; i++ {
		parentID := strings.Join(parts[:i+1], sep)
		if parentName, ok := ruleIDToName[parentID]; ok {
			loc = append(loc, Location{
				ID:   *idPrefix + parentID,
				Name: parentName,
			})
		}
	}
	return loc
}