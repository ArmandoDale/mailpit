<script>
import CommonMixins from "../mixins/CommonMixins";
import { mailbox } from "../stores/mailbox";

// Shows who is signed in and offers a way out.
//
// Renders nothing when authentication is not configured, so an unauthenticated
// Mailpit looks exactly like upstream.
export default {
	mixins: [CommonMixins],

	data() {
		return {
			mailbox,
		};
	},

	computed: {
		session() {
			return mailbox.session;
		},

		enabled() {
			return this.session.enabled && this.session.authenticated;
		},

		// The tags a project user is limited to. Empty for an administrator.
		scopeTags() {
			return this.session.tags ?? [];
		},

		scopeLabel() {
			if (this.session.admin) {
				return "Accesso completo a tutte le caselle";
			}

			if (this.scopeTags.length === 0) {
				// Worth stating plainly: a user with no project role sees only
				// untagged mail, and should be told rather than left puzzled.
				return "Nessun progetto assegnato — vedi solo le mail senza tag";
			}

			return "Progetti: " + this.scopeTags.join(", ");
		},
	},

	methods: {
		logout() {
			window.location.href = this.resolve("/auth/logout");
		},
	},
};
</script>

<template>
	<div v-if="enabled" class="d-flex align-items-center gap-2 py-1 small text-muted user-session">
		<i class="bi bi-person-circle"></i>

		<span class="text-truncate" :title="scopeLabel">
			{{ session.username }}
			<span v-if="session.admin" class="badge text-bg-secondary ms-1">admin</span>
		</span>

		<button
			class="btn btn-sm btn-link text-muted p-0 ms-auto text-nowrap"
			title="Esci e termina la sessione"
			@click="logout()"
		>
			<i class="bi bi-box-arrow-right"></i>
			Esci
		</button>
	</div>

	<div v-if="enabled && !session.admin" class="small text-muted pb-1 user-scope">
		<i class="bi bi-tags me-1"></i>
		<span v-if="scopeTags.length">{{ scopeTags.join(", ") }}</span>
		<span v-else class="fst-italic">solo mail senza tag</span>
	</div>
</template>

<style scoped>
.user-session {
	border-top: 1px solid var(--bs-border-color);
	padding-top: 0.4rem;
}
</style>
