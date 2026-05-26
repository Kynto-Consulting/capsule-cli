package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var aiCmd = &cobra.Command{
	Use:   "ai [optional prompt]",
	Short: "Interactive Bedrock AI assistant",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			// Single shot query
			prompt := strings.Join(args, " ")
			return executeAIChatPrompt(prompt)
		}

		// Interactive chat loop
		fmt.Println("🤖 Capsule AI Interactive Assistant (powered by AWS Bedrock)")
		fmt.Println("Type your infrastructure question or type 'exit' / 'quit' to close.")
		fmt.Println()
		scanner := bufio.NewScanner(os.Stdin)
		for {
			fmt.Print("You > ")
			if !scanner.Scan() {
				break
			}
			text := scanner.Text()
			if text == "exit" || text == "quit" {
				break
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			err := executeAIChatPrompt(text)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			}
			fmt.Println()
		}
		return nil
	},
}

func executeAIChatPrompt(prompt string) error {
	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := map[string]any{
		"model": "claude-haiku-4.5",
		"messages": []chatMessage{
			{Role: "user", Content: prompt},
		},
	}

	type openAIChoice struct {
		Message chatMessage `json:"message"`
	}
	var resp struct {
		Choices []openAIChoice `json:"choices"`
	}

	if err := apiClient.Post("/api/v1/ai/chat", body, &resp); err != nil {
		return err
	}

	if len(resp.Choices) > 0 {
		fmt.Println("\nClaude > " + resp.Choices[0].Message.Content)
	} else {
		fmt.Println("\nClaude > Received empty response.")
	}
	return nil
}

var aiDockerfileCmd = &cobra.Command{
	Use:   "dockerfile",
	Short: "Generate an optimized Dockerfile for a given runtime",
	RunE: func(cmd *cobra.Command, args []string) error {
		runtime, _ := cmd.Flags().GetString("runtime")
		if runtime == "" {
			return fmt.Errorf("--runtime is required (e.g. go, node, python, rust)")
		}

		body := map[string]string{"runtime": runtime}
		var resp map[string]string
		if err := apiClient.Post("/api/v1/ai/dockerfile", body, &resp); err != nil {
			return err
		}

		fmt.Println(resp["dockerfile"])
		return nil
	},
}

var aiOptimizeCostsCmd = &cobra.Command{
	Use:   "optimize-costs",
	Short: "Suggest cost-saving recommendations for a project configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		projectID, _ := cmd.Flags().GetString("project")
		if projectID == "" {
			return fmt.Errorf("--project is required")
		}

		body := map[string]string{"project_id": projectID}
		var resp map[string]string
		if err := apiClient.Post("/api/v1/ai/optimize-costs", body, &resp); err != nil {
			return err
		}

		fmt.Println(resp["recommendations"])
		return nil
	},
}

var aiKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Manage Bedrock proxy API keys",
}

var aiKeysCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Generate a new Bedrock proxy API key",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		body := map[string]string{"name": name}
		var resp map[string]any
		if err := apiClient.Post("/api/v1/ai/keys", body, &resp); err != nil {
			return err
		}

		fmt.Println("API Key generated successfully! Save it now. It will NEVER be shown again:")
		fmt.Printf("Name:   %v\n", resp["name"])
		fmt.Printf("KeyID:  %v\n", resp["id"])
		fmt.Printf("Token:  %v\n", resp["key"])
		return nil
	},
}

var aiKeysListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active Bedrock API keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		type token struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Prefix     string `json:"prefix"`
			LastUsedAt string `json:"last_used_at"`
			CreatedAt  string `json:"created_at"`
		}
		var resp struct {
			Data []token `json:"data"`
		}
		if err := apiClient.Get("/api/v1/ai/keys", &resp); err != nil {
			return err
		}

		out, _ := cmd.Flags().GetString("output")
		if out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp.Data)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPREFIX\tCREATED AT\tLAST USED AT")
		for _, t := range resp.Data {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Prefix, t.CreatedAt, t.LastUsedAt)
		}
		return w.Flush()
	},
}

var aiKeysRevokeCmd = &cobra.Command{
	Use:   "revoke [key-id]",
	Short: "Revoke/delete an active API key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		keyID := args[0]
		path := fmt.Sprintf("/api/v1/ai/keys/%s", keyID)
		if err := apiClient.Delete(path); err != nil {
			return err
		}

		fmt.Printf("Successfully revoked API key %s\n", keyID)
		return nil
	},
}

func init() {
	aiDockerfileCmd.Flags().String("runtime", "", "Runtime to generate Dockerfile for (go, node, python, rust)")
	aiOptimizeCostsCmd.Flags().String("project", "", "Project ID")
	aiKeysCreateCmd.Flags().String("name", "Default Key", "Descriptive key name")

	aiKeysCmd.AddCommand(aiKeysCreateCmd, aiKeysListCmd, aiKeysRevokeCmd)
	aiCmd.AddCommand(aiDockerfileCmd, aiOptimizeCostsCmd, aiKeysCmd)
	rootCmd.AddCommand(aiCmd)
}
