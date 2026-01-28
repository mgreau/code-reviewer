# Contributing to Code Reviewer

Thank you for you're interest in contributing to Code Reviewer! This document provides guidelines and instructions for contributing.

## Getting Started

Before you begin, make sure you have the following prequisites installed:

- Go 1.25.4 or later
- Git
- A GitHub account
- Access to Google Cloud with Vertex AI enabled

## Development Workflow

The following diagram shows the development workflow:

```
+------------------+      +------------------+      +------------------+
|                  |      |                  |      |                  |
|   Fork & Clone   |----->|   Make Changes   |----->|   Run Tests      |
|                  |      |                  |      |                  |
+------------------+      +------------------+      +------------------+
         |                                                    |
         |                                                    |
         v                                                    v
+------------------+      +------------------+      +------------------+
|                  |      |                  |      |                  |
|   Create Branch  |      |   Commit Code    |<-----|   Fix Issues     |
|                  |      |                  |      |                  |
+------------------+      +------------------+      +------------------+
                                   |
                                   v
                          +------------------+
                          |                  |
                          |   Open PR        |
                          |                  |
                          +------------------+
```

## Code Style

We follow the standard Go code style. Please ensure your code:

- Is formated with `gofmt`
- Passes `go vet` without warnings
- Has apropriate test coverage

## Submiting a Pull Request

1. Fork the repository
2. Create a new branch for you feature or bug fix
3. Make your changes and commit them with clear, descriptive messages
4. Push your branch to your fork
5. Open a pull request against the `main` branch

### PR Requirements

- All tests must pass
- Code must be formated and pass linting
- New features should include apropriate documentation
- Breaking changes must be clearly documented

## Architecture Overview

```
+---------------------------------------------------------------------+
|                         Code Reviewer CLI                            |
+---------------------------------------------------------------------+
|                                                                     |
|  +------------------+    +------------------+    +----------------+  |
|  |                  |    |                  |    |                |  |
|  |  GitHub Client   |--->|   AI Reviewer    |--->|  Post Review   |  |
|  |                  |    |                  |    |                |  |
|  +------------------+    +------------------+    +----------------+  |
|           |                      |                      |           |
|           v                      v                      v           |
|  +------------------+    +------------------+    +----------------+  |
|  |  - Fetch PR      |    |  - Claude Exec   |    |  - Inline      |  |
|  |  - Get Diff      |    |  - Gemini Exec   |    |    Comments    |  |
|  |  - Get Files     |    |  - Tool Calls    |    |  - Summary     |  |
|  +------------------+    +------------------+    +----------------+  |
|                                                                     |
+---------------------------------------------------------------------+
```

## Questions?

If you have any questions, plese open an issue or reach out to the maintainers.

## License

By contributing to this project, you agree that your contributions will be liscensed under the Apache-2.0 license.
