package web

import (
	"net/http"
	"sort"
	"strings"
)

// localAPIOperation is one documented operation of the local REST API. The
// catalogue in openapi_catalogue.go holds every operation the server serves and
// is the single input to both the generated OpenAPI document and the browsable
// reference page, so the two can never disagree.
type localAPIOperation struct {
	Group   string
	Method  string
	Path    string
	Summary string
}

// openAPIVersion is the specification version the generated document declares.
const openAPIVersion = "3.1.0"

// localAPIVersion is the version of the local API contract itself. Existing
// routes and response shapes are additive-only, so it moves with the product's
// compatibility promise rather than with the build.
const localAPIVersion = "1.1.0"

// OperationID returns a stable, unique identifier for the operation. Generators
// key their client method names on it, so it is derived deterministically from
// the method and path rather than from the summary text.
func (operation localAPIOperation) OperationID() string {
	var builder strings.Builder
	builder.WriteString(strings.ToLower(operation.Method))
	for _, segment := range strings.Split(operation.Path, "/") {
		if segment == "" || segment == "api" || segment == "v1" {
			continue
		}
		builder.WriteByte('_')
		for _, character := range segment {
			switch {
			case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
				builder.WriteRune(character)
			case character >= 'A' && character <= 'Z':
				builder.WriteRune(character + ('a' - 'A'))
			default:
				builder.WriteByte('_')
			}
		}
	}

	return strings.Trim(builder.String(), "_")
}

// PathParameters returns the {name} placeholders the path declares, in order.
func (operation localAPIOperation) PathParameters() []string {
	parameters := make([]string, 0, 2)
	for _, segment := range strings.Split(operation.Path, "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			parameters = append(parameters, strings.Trim(segment, "{}"))
		}
	}

	return parameters
}

// Mutating reports whether the operation changes local state, which decides
// whether CSRF or a full-access key is required.
func (operation localAPIOperation) Mutating() bool {
	return operation.Method != http.MethodGet && operation.Method != http.MethodHead
}

// localAPIGroup is one presentation section of the browsable reference.
type localAPIGroup struct {
	Name       string
	Operations []localAPIOperation
}

// localAPIGroups returns the catalogue grouped in catalogue order.
func localAPIGroups() []localAPIGroup {
	groups := make([]localAPIGroup, 0, 16)
	index := make(map[string]int, 16)
	for _, operation := range localAPICatalogue() {
		position, ok := index[operation.Group]
		if !ok {
			index[operation.Group] = len(groups)
			groups = append(groups, localAPIGroup{Name: operation.Group})
			position = len(groups) - 1
		}
		groups[position].Operations = append(groups[position].Operations, operation)
	}

	return groups
}

// openAPIDocument generates the OpenAPI document from the route catalogue.
func openAPIDocument() map[string]any {
	paths := make(map[string]any, len(localAPICatalogue()))
	tagNames := make([]string, 0, 16)
	seenTags := make(map[string]struct{}, 16)

	for _, operation := range localAPICatalogue() {
		if _, ok := seenTags[operation.Group]; !ok {
			seenTags[operation.Group] = struct{}{}
			tagNames = append(tagNames, operation.Group)
		}

		item, _ := paths[operation.Path].(map[string]any)
		if item == nil {
			item = make(map[string]any, 4)
			paths[operation.Path] = item
		}
		item[strings.ToLower(operation.Method)] = openAPIOperationObject(operation)
	}

	tags := make([]map[string]string, 0, len(tagNames))
	for _, name := range tagNames {
		tags = append(tags, map[string]string{"name": name})
	}

	return map[string]any{
		"openapi": openAPIVersion,
		"info": map[string]any{
			"title":       "Google Maps Scraper Local API",
			"version":     localAPIVersion,
			"description": openAPIDescription,
			"license":     map[string]string{"name": "See the repository LICENSE"},
		},
		"servers": []map[string]string{{"url": "/", "description": "This local workspace"}},
		"tags":    tags,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]string{"type": "http", "scheme": "bearer"},
				"apiKey":     map[string]string{"type": "apiKey", "in": "header", "name": "X-API-Key"},
			},
			"schemas": map[string]any{
				"Envelope": map[string]any{
					"type":        "object",
					"description": "Successful JSON responses wrap their payload in a data member.",
					"properties":  map[string]any{"data": map[string]any{}},
				},
				"Error": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"error": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"code":    map[string]string{"type": "string"},
								"message": map[string]string{"type": "string"},
							},
						},
					},
				},
			},
		},
		"security": []map[string][]string{{"bearerAuth": {}}, {"apiKey": {}}},
		"paths":    paths,
	}
}

// openAPIDescription explains the two authentication paths and the deliberate
// legacy quirks a generated client must preserve.
const openAPIDescription = "Loopback-first local API. Local API keys carry read-only or full access; " +
	"a same-origin browser session may instead present the CSRF token on mutating requests. " +
	"Job status values are the historical pending/working/ok/failed set, and duration fields " +
	"are serialized as integer nanoseconds."

func openAPIOperationObject(operation localAPIOperation) map[string]any {
	object := map[string]any{
		"operationId": operation.OperationID(),
		"summary":     operation.Summary,
		"tags":        []string{operation.Group},
		"responses":   openAPIResponses(operation),
	}

	parameters := make([]map[string]any, 0, 2)
	for _, name := range operation.PathParameters() {
		parameters = append(parameters, map[string]any{
			"name": name, "in": "path", "required": true,
			"schema": map[string]string{"type": "string"},
		})
	}
	if len(parameters) > 0 {
		object["parameters"] = parameters
	}
	if operation.Mutating() {
		object["requestBody"] = map[string]any{
			"required": false,
			"content": map[string]any{
				"application/json":                  map[string]any{"schema": map[string]string{"type": "object"}},
				"application/x-www-form-urlencoded": map[string]any{"schema": map[string]string{"type": "object"}},
			},
		}
	}
	// x-codeSamples is the widely supported vendor extension for per-operation
	// snippets; a viewer that does not understand it simply ignores it.
	samples := make([]map[string]string, 0, 4)
	for _, example := range localAPIExamples(operation, "http://127.0.0.1:8080") {
		samples = append(samples, map[string]string{"lang": example.Language, "source": example.Code})
	}
	object["x-codeSamples"] = samples

	return object
}

func openAPIResponses(operation localAPIOperation) map[string]any {
	success := "200"
	if operation.Method == http.MethodPost && strings.Count(operation.Path, "/") == 3 {
		success = "201"
	}
	responses := map[string]any{
		success: map[string]any{
			"description": "Successful local response",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]string{"$ref": "#/components/schemas/Envelope"},
				},
			},
		},
		"401": openAPIErrorResponse("Authentication is required once a local API key exists"),
		"422": openAPIErrorResponse("The request failed local validation"),
		"429": openAPIErrorResponse("The local rate limit was exceeded"),
	}
	if len(operation.PathParameters()) > 0 {
		responses["404"] = openAPIErrorResponse("The addressed local record was not found")
	}
	if operation.Mutating() {
		responses["403"] = openAPIErrorResponse("A same-origin browser request did not carry a valid CSRF token")
	}

	return responses
}

func openAPIErrorResponse(description string) map[string]any {
	return map[string]any{
		"description": description,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]string{"$ref": "#/components/schemas/Error"},
			},
		},
	}
}

// apiOpenAPI serves the generated document. It is deliberately generated on
// each request rather than cached, because the catalogue is a compiled-in slice
// and the cost is a few milliseconds on a loopback request.
func (s *Server) apiOpenAPI(w http.ResponseWriter, _ *http.Request) {
	renderJSON(w, http.StatusOK, openAPIDocument())
}

// codeExample is one runnable snippet for the browsable reference.
type codeExample struct {
	Language string
	Label    string
	Code     string
}

// localAPIExamples renders the same request in the four languages the
// specification asks for. Path placeholders are filled with an obvious sample
// so an operator can paste and edit rather than guess the shape.
func localAPIExamples(operation localAPIOperation, baseURL string) []codeExample {
	path := operation.Path
	for _, name := range operation.PathParameters() {
		path = strings.ReplaceAll(path, "{"+name+"}", "YOUR_"+strings.ToUpper(name))
	}
	target := strings.TrimRight(baseURL, "/") + path
	method := operation.Method

	curl := "curl -X " + method + " \\\n  -H \"Authorization: Bearer $GMS_API_KEY\" \\\n"
	python := "import os, requests\n\n" +
		"response = requests." + strings.ToLower(method) + "(\n    \"" + target + "\",\n" +
		"    headers={\"Authorization\": \"Bearer \" + os.environ[\"GMS_API_KEY\"]},\n"
	javascript := "const response = await fetch(\"" + target + "\", {\n" +
		"    method: \"" + method + "\",\n" +
		"    headers: { \"X-API-Key\": process.env.GMS_API_KEY"
	golang := "request, err := http.NewRequestWithContext(ctx, http.Method" + methodIdentifier(method) + ",\n" +
		"    \"" + target + "\", " + goRequestBody(operation) + ")\n" +
		"if err != nil {\n    return err\n}\n" +
		"request.Header.Set(\"Authorization\", \"Bearer \"+os.Getenv(\"GMS_API_KEY\"))\n"

	if operation.Mutating() {
		curl += "  -H \"Content-Type: application/json\" \\\n  -d '{}' \\\n"
		python += "    json={},\n"
		javascript += ", \"Content-Type\": \"application/json\" },\n    body: JSON.stringify({})"
		golang += "request.Header.Set(\"Content-Type\", \"application/json\")\n"
	} else {
		javascript += " }"
	}
	curl += "  \"" + target + "\""
	python += ")\nresponse.raise_for_status()\nprint(response.json()[\"data\"])"
	javascript += "\n});\nconst payload = await response.json();\nconsole.log(payload.data);"
	golang += "response, err := http.DefaultClient.Do(request)\n" +
		"if err != nil {\n    return err\n}\ndefer response.Body.Close()"

	return []codeExample{
		{Language: "shell", Label: "cURL", Code: curl},
		{Language: "python", Label: "Python", Code: python},
		{Language: "javascript", Label: "JavaScript", Code: javascript},
		{Language: "go", Label: "Go", Code: golang},
	}
}

func methodIdentifier(method string) string {
	if method == "" {
		return "Get"
	}

	return strings.ToUpper(method[:1]) + strings.ToLower(method[1:])
}

func goRequestBody(operation localAPIOperation) string {
	if operation.Mutating() {
		return "strings.NewReader(`{}`)"
	}

	return "http.NoBody"
}

// sortedOperationPaths is used by tests and by the reference page to present a
// deterministic ordering when a caller wants paths rather than groups.
func sortedOperationPaths() []string {
	seen := make(map[string]struct{}, len(localAPICatalogue()))
	paths := make([]string, 0, len(localAPICatalogue()))
	for _, operation := range localAPICatalogue() {
		if _, ok := seen[operation.Path]; ok {
			continue
		}
		seen[operation.Path] = struct{}{}
		paths = append(paths, operation.Path)
	}
	sort.Strings(paths)

	return paths
}
