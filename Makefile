APP     = angedelamort/plex-photos
ARTIFACT = plex-photos
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
IMAGE   = $(APP):$(VERSION)
LATEST  = $(APP):latest
OUT_DIR = dist

.PHONY: build run release clean dev

## Build l'image Docker locale
build:
	docker build \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE) \
		-t $(LATEST) \
		.
	@echo "Image: $(IMAGE)"

## Lance l'app en local pour tester (auth mock, photos d'exemple)
run: build
	docker compose up

## Lance l'app en local sans Docker, en mode auth mock
dev:
	AUTH_PROVIDER=mock \
	MOCK_USER=dev MOCK_ADMIN=true \
	DATA_PATH=./testdata/data \
	PORT=8099 \
	go run .

## Build + export TWO tarballs (version pin + latest override for Synology).
## Each file must contain ONLY its own RepoTag — Synology replaces the running
## image by matching the :latest tag, so bundling both tags in one archive
## (or saving the version tag into the "latest" file) will not update the host.
release: build
	mkdir -p $(OUT_DIR)
	docker save $(IMAGE) | gzip > $(OUT_DIR)/$(ARTIFACT)-$(VERSION).tar.gz
	docker save $(LATEST) | gzip > $(OUT_DIR)/$(ARTIFACT)-latest.tar.gz
	@echo ""
	@echo "Artefacts prêts :"
	@echo "  $(OUT_DIR)/$(ARTIFACT)-$(VERSION).tar.gz  (tag $(VERSION))"
	@echo "  $(OUT_DIR)/$(ARTIFACT)-latest.tar.gz      (tag latest)"
	@echo "Charger avec : docker load < fichier.tar.gz"
	@echo "Sur NAS (ex. Synology) : importer le *-latest.tar.gz pour remplacer l'image en cours"

## Nettoie les images et le dossier dist
clean:
	rm -rf $(OUT_DIR)
	docker rmi $(IMAGE) $(LATEST) 2>/dev/null || true
