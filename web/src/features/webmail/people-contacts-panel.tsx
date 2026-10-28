import { FormEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  addPeopleGroupMember,
  createPeopleGroup,
  deletePeopleContact,
  deletePeopleGroup,
  downloadPeopleFile,
  peopleContacts,
  peopleGroupMembers,
  peopleGroups,
  removePeopleGroupMember,
  savePeopleContact,
  type ContactDraft,
  type PeopleContact,
} from "./mailbox-people-api";

const blank: ContactDraft = {
  display_name: "",
  given_name: "",
  family_name: "",
  emails: [],
  phones: [],
  note: "",
};
const textArea =
  "block min-h-24 w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
export function PeopleContactsPanel() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const cache = useQueryClient();
  const [selected, setSelected] = useState<PeopleContact>();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<ContactDraft>(blank);
  const [emailsText, setEmailsText] = useState("");
  const [phonesText, setPhonesText] = useState("");
  const [groupFilter, setGroupFilter] = useState(0);
  const [groupName, setGroupName] = useState("");
  const [addGroup, setAddGroup] = useState(0);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState<"contact" | "group">();
  const contacts = useInfiniteQuery({
    queryKey: ["people-contacts"],
    queryFn: ({ pageParam }) => peopleContacts(pageParam),
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const groups = useQuery({
    queryKey: ["people-groups"],
    queryFn: peopleGroups,
  });
  const members = useQuery({
    queryKey: ["people-group", groupFilter],
    queryFn: () => peopleGroupMembers(groupFilter),
    enabled: groupFilter > 0,
  });
  const rows = groupFilter
    ? (members.data?.contacts ?? [])
    : (contacts.data?.pages.flatMap((page) => page.contacts) ?? []);
  const refresh = async () => {
    await Promise.all([
      cache.invalidateQueries({ queryKey: ["people-contacts"] }),
      cache.invalidateQueries({ queryKey: ["people-groups"] }),
      cache.invalidateQueries({ queryKey: ["people-group"] }),
    ]);
  };
  const run = async (action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
      await refresh();
      setNotice(t("people.saved"));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const edit = (contact?: PeopleContact) => {
    setSelected(contact);
    setEmailsText(
      (contact?.emails ?? []).map((email) => email.address).join("\n"),
    );
    setPhonesText(
      (contact?.phones ?? []).map((phone) => phone.number).join("\n"),
    );
    setDraft(
      contact
        ? {
            display_name: contact.display_name,
            given_name: contact.given_name ?? "",
            family_name: contact.family_name ?? "",
            emails: contact.emails ?? [],
            phones: contact.phones ?? [],
            note: contact.note ?? "",
          }
        : blank,
    );
    setEditing(true);
    setError("");
    setNotice("");
  };
  const save = (event: FormEvent) => {
    event.preventDefault();
    void run(async () => {
      await savePeopleContact(
        {
          ...draft,
          emails: emailsText
            .split("\n")
            .map((s) => s.trim())
            .filter(Boolean)
            .map((address) => ({
              address,
              label: draft.emails.find((e) => e.address === address)?.label,
            })),
          phones: phonesText
            .split("\n")
            .map((s) => s.trim())
            .filter(Boolean)
            .map((number) => ({
              number,
              label: draft.phones.find((p) => p.number === number)?.label,
            })),
        },
        selected,
      );
      setEditing(false);
      setSelected(undefined);
    });
  };
  const filteredGroup = groups.data?.groups.find(
    (group) => group.id === groupFilter,
  );
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">
            {t("page.webmail.contacts_title")}
          </h2>
          <p className="text-sm text-muted-foreground">
            {t("people.contacts_help")}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button disabled={busy} onClick={() => edit()}>
            {t("page.webmail.contact_add")}
          </Button>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() =>
              void run(() =>
                downloadPeopleFile("/contacts/export.vcf", "contacts.vcf"),
              )
            }
          >
            {t("people.export_contacts")}
          </Button>
        </div>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
      <div className="flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-sm">
          {t("people.group_filter")}
          <select
            className="h-9 max-w-full rounded-md border bg-background px-3"
            aria-label={t("people.group_filter")}
            value={groupFilter}
            onChange={(e) => setGroupFilter(Number(e.target.value))}
          >
            <option value={0}>{t("people.all_contacts")}</option>
            {(groups.data?.groups ?? []).map((group) => (
              <option key={group.id} value={group.id}>
                {group.name}
              </option>
            ))}
          </select>
        </label>
        {filteredGroup && (
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => setConfirm("group")}
          >
            {t("people.delete_group")}
          </Button>
        )}
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            void run(async () => {
              await createPeopleGroup(groupName);
              setGroupName("");
            });
          }}
        >
          <label className="grid gap-1 text-sm">
            {t("people.group_name")}
            <Input
              required
              maxLength={100}
              value={groupName}
              onChange={(e) => setGroupName(e.target.value)}
            />
          </label>
          <Button
            type="submit"
            variant="outline"
            disabled={busy || !groupName.trim()}
          >
            {t("people.create_group")}
          </Button>
        </form>
      </div>
      <div className={editing ? "grid gap-6 lg:grid-cols-2" : "space-y-3"}>
        <section
          aria-label={t("people.contact_list")}
          className="min-w-0 space-y-3"
        >
          {(contacts.isLoading || members.isFetching) && (
            <div
              aria-label={t("state.loading")}
              className="h-24 animate-pulse rounded-md bg-muted"
            />
          )}
          {(contacts.error || groups.error || members.error) && (
            <p role="alert">
              {errorMessage(contacts.error || groups.error || members.error)}
            </p>
          )}
          {!contacts.isLoading && rows.length === 0 && (
            <p className="py-8 text-sm text-muted-foreground">
              {t("people.contacts_empty")}
            </p>
          )}
          <ul className="divide-y">
            {rows.map((contact) => (
              <li
                key={contact.id}
                className="flex flex-wrap items-center justify-between gap-3 py-3"
              >
                <div className="min-w-0">
                  <button
                    type="button"
                    className="break-words text-left font-medium underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    onClick={() => edit(contact)}
                  >
                    {contact.display_name}
                  </button>
                  <p className="break-words text-sm text-muted-foreground">
                    {(contact.emails ?? [])
                      .map((email) => email.address)
                      .join(", ")}
                  </p>
                  <p className="text-sm text-muted-foreground">
                    {(contact.phones ?? [])
                      .map((phone) => phone.number)
                      .join(", ")}
                  </p>
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => edit(contact)}
                  >
                    {t("action.edit")}
                  </Button>
                  {filteredGroup && (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busy}
                      onClick={() =>
                        void run(() =>
                          removePeopleGroupMember(filteredGroup, contact),
                        )
                      }
                    >
                      {t("people.remove_from_group")}
                    </Button>
                  )}
                </div>
              </li>
            ))}
          </ul>
          {!groupFilter && contacts.hasNextPage && (
            <Button
              variant="outline"
              disabled={contacts.isFetchingNextPage}
              onClick={() => void contacts.fetchNextPage()}
            >
              {t("people.load_more")}
            </Button>
          )}
        </section>
        {editing && (
          <section
            className="min-w-0 space-y-4 rounded-md border p-4"
            aria-label={
              selected
                ? t("people.edit_contact")
                : t("page.webmail.contact_add")
            }
          >
            <h3 className="font-semibold">
              {selected
                ? t("people.edit_contact")
                : t("page.webmail.contact_add")}
            </h3>
            <form className="space-y-3" onSubmit={save}>
              <label className="grid gap-1 text-sm">
                {t("page.webmail.contact_name")}
                <Input
                  required
                  maxLength={200}
                  value={draft.display_name}
                  onChange={(e) =>
                    setDraft({ ...draft, display_name: e.target.value })
                  }
                />
              </label>
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="grid gap-1 text-sm">
                  {t("people.given_name")}
                  <Input
                    maxLength={100}
                    value={draft.given_name}
                    onChange={(e) =>
                      setDraft({ ...draft, given_name: e.target.value })
                    }
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("people.family_name")}
                  <Input
                    maxLength={100}
                    value={draft.family_name}
                    onChange={(e) =>
                      setDraft({ ...draft, family_name: e.target.value })
                    }
                  />
                </label>
              </div>
              <label className="grid gap-1 text-sm">
                {t("people.emails")}
                <textarea
                  className={textArea}
                  value={emailsText}
                  onChange={(e) => setEmailsText(e.target.value)}
                />
              </label>
              <label className="grid gap-1 text-sm">
                {t("people.phones")}
                <textarea
                  className={textArea}
                  value={phonesText}
                  onChange={(e) => setPhonesText(e.target.value)}
                />
              </label>
              <label className="grid gap-1 text-sm">
                {t("people.note")}
                <textarea
                  className={textArea}
                  maxLength={4000}
                  value={draft.note}
                  onChange={(e) => setDraft({ ...draft, note: e.target.value })}
                />
              </label>
              <div className="flex flex-wrap gap-2">
                <Button disabled={busy} type="submit">
                  {t("action.save")}
                </Button>
                <Button
                  variant="outline"
                  disabled={busy}
                  type="button"
                  onClick={() => setEditing(false)}
                >
                  {t("action.cancel")}
                </Button>
                {selected && (
                  <Button
                    variant="destructive"
                    type="button"
                    disabled={busy}
                    onClick={() => setConfirm("contact")}
                  >
                    {t("action.delete")}
                  </Button>
                )}
              </div>
            </form>
            {selected && (
              <form
                className="flex flex-wrap items-end gap-2 border-t pt-4"
                onSubmit={(event) => {
                  event.preventDefault();
                  const group = groups.data?.groups.find(
                    (item) => item.id === addGroup,
                  );
                  if (group)
                    void run(() => addPeopleGroupMember(group, selected));
                }}
              >
                <label className="grid gap-1 text-sm">
                  {t("people.add_to_group")}
                  <select
                    className="h-9 max-w-full rounded-md border bg-background px-3"
                    required
                    aria-label={t("people.add_to_group")}
                    value={addGroup}
                    onChange={(e) => setAddGroup(Number(e.target.value))}
                  >
                    <option value={0}>{t("people.choose_group")}</option>
                    {(groups.data?.groups ?? [])
                      .filter(
                        (group) =>
                          group.addressbook_id === selected.addressbook_id,
                      )
                      .map((group) => (
                        <option key={group.id} value={group.id}>
                          {group.name}
                        </option>
                      ))}
                  </select>
                </label>
                <Button variant="outline" disabled={busy || !addGroup}>
                  {t("people.add_to_group")}
                </Button>
              </form>
            )}
          </section>
        )}
      </div>
      <Dialog
        open={!!confirm}
        onOpenChange={(open) => {
          if (!open) setConfirm(undefined);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t(
                confirm === "group"
                  ? "people.delete_group"
                  : "people.delete_contact",
              )}
            </DialogTitle>
            <DialogDescription>
              {t(
                confirm === "group"
                  ? "people.delete_group_help"
                  : "people.delete_contact_help",
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setConfirm(undefined)}
            >
              {t("action.cancel")}
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  if (confirm === "contact" && selected) {
                    await deletePeopleContact(selected);
                    setEditing(false);
                    setSelected(undefined);
                  } else if (confirm === "group" && groupFilter) {
                    await deletePeopleGroup(groupFilter);
                    setGroupFilter(0);
                  }
                  setConfirm(undefined);
                })
              }
            >
              {t("action.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
