FILES=remora.svg architecture.svg remora-dark.svg remora-future.svg frontier.svg

all: $(FILES)

%.svg: %.drawio
	drawio \
		--export \
		--format svg \
		--svg-theme light \
		--output $@ $<

%-dark.svg: %.drawio
	drawio \
		--export \
		--format svg \
		--svg-theme dark \
		--output $@ $<

.PHONY: clean
clean:
	$(RM) *.svg

.PHONY: install
install: $(FILES)
	mkdir -p ~/dev/web/harrybrwn.github.io/src/img/remora/
	cp $^ ~/dev/web/harrybrwn.github.io/src/img/remora/

