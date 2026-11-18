# Builds the static guest init + agent. Needs gcc and static libc (apt: gcc libc6-dev).
mvm-guest: mvm-guest.c
	gcc -static -O2 -s -Wall -o $@ $<
clean:
	rm -f mvm-guest
