FROM squidfunk/mkdocs-material:latest@sha256:51b87149d227691486b5f08993d28c65ca7e4990010664b697265b8e6fcd5287

RUN pip install pymdown-extensions

# No USER directive: `make docs` bind-mounts the whole repo (-v ${PWD}:/docs)
# with no UID mapping, and the host directory's owning UID varies per
# developer/CI runner. Baking in a fixed non-root UID risks permission-denied
# reads of the bind-mounted docs on any host whose UID doesn't match.
