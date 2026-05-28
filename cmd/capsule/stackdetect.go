package main

import (
	"fmt"
	"strings"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// generateDockerfile returns Dockerfile content for a given lang/stack.
// Returns "" if we can't determine the stack.
func generateDockerfile(lang string, pc *config.ProjectConfig) string {
	switch lang {
	case "Go":
		return goDockerfile(pc)
	case "Node.js", "Node.js (static)":
		return nodeDockerfile(pc)
	case "Next.js":
		return nextjsDockerfile(pc)
	case "Python":
		return pythonDockerfile(pc)
	case "Ruby":
		return rubyDockerfile(pc)
	case "Java":
		return javaDockerfile(pc)
	default:
		return ""
	}
}

func goDockerfile(_ *config.ProjectConfig) string {
	return `FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o server .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
`
}

func nodeDockerfile(pc *config.ProjectConfig) string {
	cmd := `["node", "index.js"]`
	if pc != nil && pc.StartCommand != "" {
		// Convert shell command to JSON array for CMD
		parts := strings.Fields(pc.StartCommand)
		quoted := make([]string, len(parts))
		for i, p := range parts {
			quoted[i] = fmt.Sprintf("%q", p)
		}
		cmd = "[" + strings.Join(quoted, ", ") + "]"
	}
	return fmt.Sprintf(`FROM node:20-alpine AS deps
WORKDIR /app
COPY package*.json ./
RUN npm ci --only=production

FROM node:20-alpine
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
EXPOSE 3000
CMD %s
`, cmd)
}

func nextjsDockerfile(_ *config.ProjectConfig) string {
	return `FROM node:20-alpine AS deps
WORKDIR /app
COPY package*.json ./
RUN npm ci

FROM node:20-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

FROM node:20-alpine AS runner
WORKDIR /app
ENV NODE_ENV production
COPY --from=builder /app/.next ./.next
COPY --from=builder /app/public ./public
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/package.json ./package.json
EXPOSE 3000
CMD ["npm", "start"]
`
}

func pythonDockerfile(pc *config.ProjectConfig) string {
	cmd := `["python", "app.py"]`
	if pc != nil && pc.StartCommand != "" {
		parts := strings.Fields(pc.StartCommand)
		quoted := make([]string, len(parts))
		for i, p := range parts {
			quoted[i] = fmt.Sprintf("%q", p)
		}
		cmd = "[" + strings.Join(quoted, ", ") + "]"
	}
	return fmt.Sprintf(`FROM python:3.12-slim AS builder
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt

FROM python:3.12-slim
WORKDIR /app
COPY --from=builder /install /usr/local
COPY . .
EXPOSE 8000
CMD %s
`, cmd)
}

func rubyDockerfile(_ *config.ProjectConfig) string {
	return `FROM ruby:3.3-slim
WORKDIR /app
COPY Gemfile Gemfile.lock ./
RUN bundle install --without development test
COPY . .
EXPOSE 3000
CMD ["ruby", "app.rb"]
`
}

func javaDockerfile(_ *config.ProjectConfig) string {
	return `FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /app
COPY pom.xml .
RUN mvn dependency:go-offline
COPY src ./src
RUN mvn package -DskipTests

FROM eclipse-temurin:21-jre-alpine
WORKDIR /app
COPY --from=build /app/target/*.jar app.jar
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "app.jar"]
`
}
