PYTHON_DIR := logmate-client-python
PYTHON_DIST ?= $(PYTHON_DIR)/dist
ACTIVE_VENV_PYTHON := $(VIRTUAL_ENV)/bin/python
LOCAL_VENV_PYTHON := $(abspath $(PYTHON_DIR)/venv/bin/python)
PYTHON ?= $(if $(and $(VIRTUAL_ENV),$(wildcard $(ACTIVE_VENV_PYTHON))),$(ACTIVE_VENV_PYTHON),$(if $(wildcard $(LOCAL_VENV_PYTHON)),$(LOCAL_VENV_PYTHON),python3))
TYPESCRIPT_DIR := logmate-client-ts
NPM ?= npm

.PHONY: help python-tools python-clean python-build python-check \
	python-publish-test python-publish python-require-tools \
	typescript-tools typescript-clean typescript-build typescript-test \
	typescript-check typescript-version typescript-publish-staging \
	typescript-publish

help:
	@echo "Python package targets:"
	@echo "  make python-tools         Install build and Twine"
	@echo "  make python-build         Build wheel and source archive"
	@echo "  make python-check         Validate generated archives"
	@echo "  make python-publish-test  Publish to TestPyPI"
	@echo "  make python-publish       Publish to production PyPI"
	@echo ""
	@echo "TypeScript package targets:"
	@echo "  make typescript-tools     Install dependencies from package-lock.json"
	@echo "  make typescript-build     Build ESM, CommonJS, and declarations"
	@echo "  make typescript-test      Run the TypeScript test suite"
	@echo "  make typescript-check     Typecheck, test, build, and inspect package"
	@echo "  make typescript-version VERSION=0.2.0  Change the project version"
	@echo "  make typescript-publish-staging  Generate and publish a prerelease on npm tag next"
	@echo "  make typescript-publish          Publish stable version on npm tag latest"

python-tools:
	$(PYTHON) -m pip install --upgrade build twine

python-require-tools:
	@$(PYTHON) -c 'import build, twine' >/dev/null 2>&1 || { \
		echo "Python packaging tools are missing in: $(PYTHON)"; \
		echo "Run: make python-tools"; \
		exit 1; \
	}

python-clean:
	rm -rf "$(PYTHON_DIST)" "$(PYTHON_DIR)/build"

python-build: python-require-tools python-clean
	$(PYTHON) -m build --outdir "$(abspath $(PYTHON_DIST))" "$(PYTHON_DIR)"

python-check: python-build
	$(PYTHON) -m twine check "$(PYTHON_DIST)"/*

python-publish-test: python-check
	$(PYTHON) -m twine upload --repository testpypi "$(PYTHON_DIST)"/*

python-publish: python-check
	$(PYTHON) -m twine upload --repository pypi "$(PYTHON_DIST)"/*

typescript-tools:
	cd "$(TYPESCRIPT_DIR)" && $(NPM) ci

typescript-clean:
	cd "$(TYPESCRIPT_DIR)" && $(NPM) run clean

typescript-build:
	cd "$(TYPESCRIPT_DIR)" && $(NPM) run build

typescript-test:
	cd "$(TYPESCRIPT_DIR)" && $(NPM) test

typescript-check:
	cd "$(TYPESCRIPT_DIR)" && $(NPM) run check

typescript-version:
	@test -n "$(VERSION)" || { echo "VERSION is required (example: VERSION=0.2.0-rc.1)"; exit 1; }
	cd "$(TYPESCRIPT_DIR)" && $(NPM) version "$(VERSION)" --no-git-tag-version

typescript-publish-staging: typescript-check
	@set -eu; \
	base_version="$$(cd "$(TYPESCRIPT_DIR)" && node -p "require('./package.json').version.split('-')[0]")"; \
	version="$(VERSION)"; \
	if test -z "$$version"; then version="$$base_version-rc.$$(node -p 'Date.now()')"; fi; \
	case "$$version" in *-*) ;; *) echo "Staging version must be a prerelease such as 0.2.0-rc.1"; exit 1;; esac; \
	stage_dir="$$(mktemp -d /tmp/logmate-npm-stage.XXXXXX)"; \
	trap 'rm -rf "$$stage_dir"' EXIT HUP INT TERM; \
	echo "Preparing @loghill-oss/logmate-client@$$version for npm tag next"; \
	cd "$(TYPESCRIPT_DIR)"; \
	LOGMATE_BUILD_VERSION="$$version" ./node_modules/.bin/tsup --out-dir "$$stage_dir/dist"; \
	cp README.md package.json "$$stage_dir/"; \
	cp -R assets "$$stage_dir/assets"; \
	node -e 'const fs=require("node:fs"); const path=process.argv[1]; const value=JSON.parse(fs.readFileSync(path,"utf8")); value.version=process.argv[2]; fs.writeFileSync(path, JSON.stringify(value,null,2)+"\n")' "$$stage_dir/package.json" "$$version"; \
	cd "$$stage_dir"; \
	$(NPM) publish --access public --tag next; \
	echo "Published @loghill-oss/logmate-client@$$version (install with: npm install @loghill-oss/logmate-client@next)"

typescript-publish: typescript-check
	@version="$$(cd "$(TYPESCRIPT_DIR)" && node -p "require('./package.json').version")"; \
	case "$$version" in *-*) echo "Production requires a stable version such as 0.2.0"; exit 1;; esac
	cd "$(TYPESCRIPT_DIR)" && $(NPM) publish --access public --tag latest
