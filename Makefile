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

## Lance l'app en local pour tester
run: build
	docker compose up

## Lance l'app en local sans Docker, en mode auth mock
dev:
	AUTH_PROVIDER=mock \
	MOCK_USER=dev MOCK_ADMIN=true \
	PHOTOS_PATH=./testdata/photos \
	DATA_PATH=./testdata/data \
	PORT=8099 \
	go run .

## Build + exporte un .tar.gz prêt pour Synology
release: build
	mkdir -p $(OUT_DIR)
	docker save $(IMAGE) $(LATEST) | gzip > $(OUT_DIR)/$(ARTIFACT)-$(VERSION).tar.gz
	@echo ""
	@echo "Prêt pour Synology : $(OUT_DIR)/$(ARTIFACT)-$(VERSION).tar.gz"
	@echo "Container Manager → Registry → Ajouter → sélectionner le fichier"

## Nettoie les images et le dossier dist
clean:
	rm -rf $(OUT_DIR)
	docker rmi $(IMAGE) $(LATEST) 2>/dev/null || true
