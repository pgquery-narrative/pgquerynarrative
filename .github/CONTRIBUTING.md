# Contributing to PgQueryNarrative

Thank you for your interest in contributing! This document provides guidelines and instructions for contributing to the project.

## Code of Conduct

By participating in this project, you agree to maintain a respectful and inclusive environment for all contributors.

## How to Contribute

### Reporting Bugs

1. Check if the bug has already been reported in [Issues](https://github.com/pgquery-narrative/pgquerynarrative/issues)
2. If not, create a new issue with:
   - Clear title and description
   - Steps to reproduce
   - Expected vs actual behavior
   - Environment details (OS, Go version, etc.)
   - Relevant logs or error messages

### Suggesting Features

1. Check existing feature requests
2. Create an issue with:
   - Clear description of the feature
   - Use case and benefits
   - Proposed implementation (if any)

### Contributing Code

#### 1. Fork and Clone
```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
```

Note: the Go module path stays `github.com/pgquerynarrative/pgquerynarrative`, only
the GitHub organization (used for clone/browser URLs) is `pgquery-narrative`. Do not
rewrite Go import paths to match.

#### 2. Create a Branch
```bash
git checkout -b feature/your-feature-name
# or
git checkout -b fix/your-bug-fix
```

#### 3. Set Up Development Environment
```bash
# Install dependencies
make setup

# Generate code
make generate

# Run tests
make test
```

#### 4. Make Changes
- Follow Go coding standards
- Write tests for new features
- Update documentation
- Keep commits focused and atomic

#### 5. Commit Changes
Use [Conventional Commits](https://www.conventionalcommits.org/):
```bash
git commit -m "feat: add new query validation rule"
git commit -m "fix: resolve timeout issue in query runner"
git commit -m "docs: update API documentation"
```

Commit types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation
- `style`: Code style (formatting)
- `refactor`: Code refactoring
- `test`: Tests
- `chore`: Maintenance

#### 6. Push and Create Pull Request
```bash
git push origin feature/your-feature-name
```

Then create a PR on GitHub with:
- Clear title and description
- Link the issue in PR body using `Closes #<issue-id>`
- Screenshots (if UI changes)
- Test results

## Development Guidelines

### Code Style
- Follow [Effective Go](https://go.dev/doc/effective_go)
- Run `make fmt` before committing
- Run `make lint` to check code quality
- Keep functions focused and small
- Add godoc comments for exported functions

### Testing
- Write unit tests for new features
- Maintain or improve test coverage
- Run all tests before submitting: `make test`
- Test edge cases and error conditions

### Documentation
- Update README if needed
- Add/update godoc comments
- Update the matching [reference page](https://pgquery-narrative.github.io/pgquerynarrative/reference/api/)
  in the same change for a config default, API route, or error code you touched,
  see [Change workflows](https://pgquery-narrative.github.io/pgquerynarrative/development/change-workflows/).
  `make docs-contract-check` fails a PR that misses this
- Include examples for new features

Previewing docs locally: `make docs` builds the Docker-based MkDocs image and serves
it at <http://127.0.0.1:8000> with the repository bind-mounted, so edits under `docs/`
show up on a page refresh. No local Python or MkDocs install is required. Before
committing a documentation change, run:

```bash
make docs-check          # mkdocs build --strict; fails on broken nav/links/build errors
make docs-contract-check # facts the docs state that the code decides (versions, config
                          # defaults, API surface, error codes, forbidden vocabulary)
make docs-links          # external link check (requires network access)
```

`make verify-pqn-docs` additionally executes the `pqn` extension quick start exactly as
written in `docs/getting-started/pqn-extension.md`; run it when touching that page or
the extension itself.


## Pull Request Process

1. **Update Documentation**: Ensure README and docs are updated
2. **Add Tests**: New features need tests
3. **Run Tests**: All tests must pass
4. **Check Linting**: Code must pass linting
5. **Update CHANGELOG**: Document your changes (if applicable)
6. **Request Review**: Assign reviewers
7. **Wait for checks**: Merge only after all required checks pass

### PR Checklist
- [ ] Code follows style guidelines
- [ ] Tests added/updated and passing
- [ ] Documentation updated
- [ ] No breaking changes (or documented)
- [ ] Commit messages follow conventions

## Review Process

- Maintainers review pull requests as time allows; there is no fixed SLA
- Address review comments promptly
- Be open to feedback and suggestions
- Keep PRs focused and reasonably sized

## Questions?

- Open an issue for questions
- Check existing documentation
- Review code examples in the codebase

Thank you for contributing!
