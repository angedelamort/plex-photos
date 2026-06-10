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

## Build + exporte un .tar.gz de l'image (chargeable sur tout hôte Docker)
release: build
	mkdir -p $(OUT_DIR)
	docker save $(IMAGE) $(LATEST) | gzip > $(OUT_DIR)/$(ARTIFACT)-$(VERSION).tar.gz
	@echo ""
	@echo "Artefact prêt : $(OUT_DIR)/$(ARTIFACT)-$(VERSION).tar.gz"
	@echo "Charger avec : docker load < $(OUT_DIR)/$(ARTIFACT)-$(VERSION).tar.gz"
	@echo "Sur NAS (ex. Synology) : Container Manager → Image → Ajouter → depuis fichier"

## Nettoie les images et le dossier dist
clean:
	rm -rf $(OUT_DIR)
	docker rmi $(IMAGE) $(LATEST) 2>/dev/null || true
