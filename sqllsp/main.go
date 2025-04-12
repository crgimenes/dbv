package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type Request struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type CompletionItem struct {
	Label string `json:"label"`
	Kind  int    `json:"kind"`
}

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type CompletionContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
}

type CompletionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      CompletionContext      `json:"context,omitzero"`
}

var documents = make(map[string]string)

func readMessage(reader *bufio.Reader) ([]byte, error) {
	var contentLength int
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				contentLength, err = strconv.Atoi(strings.TrimSpace(parts[1]))
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("invalid Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeMessage(response []byte) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(response))
	if _, err := os.Stdout.Write([]byte(header)); err != nil {
		return err
	}
	_, err := os.Stdout.Write(response)
	return err
}

func analyzeContextAndGenerateCompletions(docContent string, pos Position) []CompletionItem {

	baseKeywords := []string{"SELECT", "FROM", "WHERE", "INSERT", "UPDATE", "DELETE"}
	completions := []CompletionItem{}
	for _, keyword := range baseKeywords {
		completions = append(completions, CompletionItem{Label: keyword, Kind: 14})
	}

	return completions
}

func handleRequest(req *Request) error {
	switch req.Method {
	case "initialize":
		res := Response{
			Jsonrpc: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"capabilities": map[string]any{
					"completionProvider": map[string]any{
						"resolveProvider":   false,
						"triggerCharacters": []string{"."},
					},
				},
			},
		}
		response, err := json.Marshal(res)
		if err != nil {
			return err
		}
		return writeMessage(response)
	case "shutdown":
		res := Response{
			Jsonrpc: "2.0",
			ID:      req.ID,
			Result:  nil,
		}
		response, err := json.Marshal(res)
		if err != nil {
			return err
		}
		return writeMessage(response)
	case "textDocument/didOpen":
		var params struct {
			TextDocument struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return err
		}
		documents[params.TextDocument.URI] = params.TextDocument.Text
		return nil

	case "textDocument/didChange":
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return err
		}
		if len(params.ContentChanges) > 0 {
			documents[params.TextDocument.URI] = params.ContentChanges[0].Text
		}
		return nil
	case "textDocument/completion":
		var params CompletionParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return err
		}
		docContent, ok := documents[params.TextDocument.URI]
		if !ok {
			docContent = ""
		}
		completions := analyzeContextAndGenerateCompletions(docContent, params.Position)
		res := Response{
			Jsonrpc: "2.0",
			ID:      req.ID,
			Result:  completions,
		}
		response, err := json.Marshal(res)
		if err != nil {
			return err
		}
		return writeMessage(response)
	case "initialized", "exit":
		return nil
	default:
		if len(req.ID) > 0 && string(req.ID) != "null" {
			res := Response{
				Jsonrpc: "2.0",
				ID:      req.ID,
				Error: &ResponseError{
					Code:    -32601,
					Message: "Method not found",
				},
			}
			response, err := json.Marshal(res)
			if err != nil {
				return err
			}
			return writeMessage(response)
		}
		return nil
	}
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	for {
		data, err := readMessage(reader)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error reading message:", err)
			break
		}
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			fmt.Fprintln(os.Stderr, "Error parsing JSON:", err)
			continue
		}
		if err := handleRequest(&req); err != nil {
			fmt.Fprintln(os.Stderr, "Error handling request:", err)
		}
	}
}
