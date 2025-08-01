# API Tool - Go Web Framework Route Analyzer

## Project Overview

This is a Go CLI tool that analyzes Go web applications to extract API route information. It supports both Gin and Iris frameworks and generates structured JSON output containing route paths, HTTP methods, handlers, and request/response schemas.

## Architecture

### Core Components

1. **Parser** (`pkg/parser/`) - Go AST parsing and project analysis
2. **Extractor** (`pkg/extractor/`) - Framework-specific route extraction
3. **Analyzer** (`pkg/analyzer/`) - Core business logic for route analysis
4. **Models** (`pkg/models/`) - Data structures for API information

### Key Design Patterns

- **Strategy Pattern**: Framework-specific extractors implement the `Extractor` interface
- **Two-Phase Analysis**: 
  1. Index router group functions
  2. Recursively parse routes with context tracking
- **AST-based Analysis**: Uses Go's AST parsing with `golang.org/x/tools/go/packages`

## Build System

### Dependencies
- Go 1.20+ (specified in go.mod)
- Main framework: `github.com/gin-gonic/gin v1.10.1`
- AST tools: `golang.org/x/tools v0.6.0`

### Build Commands
```bash
# Build the main tool
go build -o api-tool ./cmd/my-tool

# Run analysis on a project
./api-tool -framework gin -path ./example
./api-tool -framework iris -path /path/to/project
```

### No Testing Framework
- No test files found (no `*_test.go` files)
- No testing infrastructure in place

## Directory Structure

```
/
├── cmd/my-tool/           # Main CLI application entry point
├── pkg/
│   ├── analyzer/          # Core analysis logic
│   ├── extractor/         # Framework extractors (Gin, Iris)
│   ├── models/            # Data models and structs
│   └── parser/            # Go AST parsing utilities
├── example/               # Example Gin application for testing
│   ├── router/            # Route definitions
│   └── sevice/           # Service layer with DTOs
├── helper/                # Utility functions
├── resp/                  # Response handling utilities
└── vendor/               # Vendored dependencies
```

## Key Technologies

- **AST Analysis**: Deep Go source code parsing
- **Framework Support**: Gin and Iris web frameworks
- **Route Extraction**: Recursive route group analysis
- **JSON Output**: Structured API documentation generation

## Development Workflow

### Primary Entry Point
- `cmd/my-tool/main.go` - CLI tool with flag parsing
- Supports both `-path` and `-framework` flags
- Can analyze any Go project containing Gin/Iris routes

### Core Analysis Flow
1. Parse Go project using `packages.Load()`
2. Initialize framework-specific extractor
3. Find root routers (`gin.Default()`, `gin.New()`)
4. Index router group functions
5. Recursively analyze route registrations
6. Extract handler information and generate JSON

### Framework Extractors

#### Gin Extractor (`pkg/extractor/gin_extractor.go`)
- Detects `gin.Engine` and `gin.RouterGroup` types
- Recognizes HTTP method calls (GET, POST, PUT, DELETE, etc.)
- Handles route groups via `.Group()` calls
- Extracts path segments from string literals

#### Iris Extractor (Referenced but implementation in `iris_extractor.go`)
- Similar pattern for Iris framework support

### Data Models

#### Core Types
- `APIInfo`: Top-level container for all routes
- `RouteInfo`: Individual route with method, path, handler
- `RequestInfo`/`ResponseInfo`: Request/response schemas
- `FieldInfo`: Structured field information with JSON tags

#### Advanced Features
- Response function analysis with call chains
- Branch context tracking for conditional responses
- Router group function indexing
- Type resolution for Go structs

## Example Usage

The `example/` directory contains a working Gin application:
- Routes defined in `example/router/router.go`
- Handlers in `example/router/api.go`
- Service layer with response helpers in `example/sevice/dto.go`

```go
// Example route group
func InitRouter(r *gin.Engine) {
    user := r.Group("/user")
    {
        user.GET("/info", GetUserInfo)
        user.GET("/book", BookInfo)
        user.GET("/users", GetUsers)
    }
}
```

## Development Environment

### Git Configuration
- Main branch: `main`
- Recent commits focus on response handling and router improvements
- `.gitignore` excludes build artifacts, logs, and debug files

### IDE Support
- `.vscode/` directory present (empty)
- No specific Cursor rules or Copilot instructions found

## Key Limitations & Notes

1. **No Tests**: No test coverage or testing framework setup
2. **Vendor Directory**: Dependencies are vendored (`vendor/`)
3. **Chinese Comments**: Some debug output and comments in Chinese
4. **Response Analysis**: Limited request/response body analysis (returns empty structs)
5. **Handler Analysis**: Currently only extracts handler names, not full function body analysis

## Development Tips

1. **AST Debugging**: Use the debug output to understand route parsing flow
2. **Framework Extension**: New frameworks can be added by implementing the `Extractor` interface
3. **Type Resolution**: The `TypeResolver` callback pattern allows for extensible type analysis
4. **Route Context**: The recursive analysis maintains parent path context for nested route groups

## Output Format

The tool generates JSON with this structure:
```json
{
  "routes": [
    {
      "method": "GET",
      "path": "/user/info",
      "handler": "GetUserInfo",
      "request": {...},
      "response": {...}
    }
  ]
}
```

This codebase is well-structured for static analysis of Go web applications, with a clear separation of concerns and extensible architecture for supporting multiple web frameworks.