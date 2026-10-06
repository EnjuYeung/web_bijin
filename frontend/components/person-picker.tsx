"use client";

import { useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Combobox } from "@base-ui/react/combobox";
import { Check, ChevronDown, Plus, X } from "lucide-react";
import type { Person } from "@/lib/api";
import { cleanName, nameKey } from "@/lib/albums";

interface Option { name: string; albums?: number; create?: boolean }

// PersonPicker chooses existing names or adds the typed one. Authors take one
// name, models several; onChange receives the complete new list of names.
// Filters pass create={false}: they only pick from names already in use.
export function PersonPicker({ id, label, placeholder, people, value, multiple = false, create = true, disabled = false, onChange }: {
  id?: string; label: string; placeholder: string; people: Person[]; value: string[]; multiple?: boolean; create?: boolean; disabled?: boolean;
  onChange: (names: string[]) => void;
}) {
  const [query, setQuery] = useState("");
  const highlighted = useRef<Option | undefined>(undefined);
  const options = useMemo<Option[]>(() => people.map(person => ({ name: person.name, albums: person.albums })), [people]);
  const selected = useMemo(() => value.map(name => options.find(option => nameKey(option.name) === nameKey(name)) ?? { name }), [value, options]);
  const typed = cleanName(query);
  const known = !typed || [...options, ...selected].some(option => nameKey(option.name) === nameKey(typed));
  const items: Option[] = known || !create ? options : [...options, { name: typed, create: true }];
  const shown = !multiple && selected[0] ? nameKey(selected[0].name) : "";

  function choose(next: Option[]) {
    const names: string[] = [];
    for (const option of next) {
      const name = cleanName(option.name);
      if (name && !names.some(other => nameKey(other) === nameKey(name))) names.push(name);
    }
    onChange(multiple ? names : names.slice(-1));
  }
  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    // Enter with no highlighted option takes the typed name, existing or new.
    if (event.key !== "Enter" || highlighted.current || !typed || nameKey(typed) === shown) return;
    const existing = options.find(item => nameKey(item.name) === nameKey(typed));
    if (!existing && !create) return;
    event.preventDefault();
    if (multiple && selected.some(option => nameKey(option.name) === nameKey(typed))) { setQuery(""); return; }
    const option = existing ?? { name: typed };
    choose(multiple ? [...selected, option] : [option]);
    if (multiple) setQuery("");
  }
  const shared = {
    items,
    disabled,
    autoHighlight: true,
    // A chosen author's name stays in the input; the whole list shows then.
    filter: (option: Option, text: string) => {
      const wanted = nameKey(text);
      return !wanted || wanted === shown || !!option.create || nameKey(option.name).includes(wanted);
    },
    itemToStringLabel: (option: Option) => option.name,
    isItemEqualToValue: (a: Option, b: Option) => nameKey(a.name) === nameKey(b.name),
    onItemHighlighted: (option: Option | undefined) => { highlighted.current = option; },
  };
  const popup = <Combobox.Portal>
    <Combobox.Positioner className="person-positioner" sideOffset={4}>
      <Combobox.Popup className="person-popup">
        <Combobox.Empty className="person-empty">{create ? "还没有名字，输入后按回车添加" : "没有匹配的名字"}</Combobox.Empty>
        <Combobox.List>
          {(option: Option) => <Combobox.Item key={(option.create ? "+" : "") + option.name} value={option} className="person-option" data-create={option.create ? "" : undefined}>
            {option.create ? <Plus aria-hidden="true" /> : <Combobox.ItemIndicator className="person-check"><Check aria-hidden="true" /></Combobox.ItemIndicator>}
            <span className="person-option-name">{option.create ? `添加「${option.name}」` : option.name}</span>
            {!option.create && !!option.albums && <small>{option.albums} 本</small>}
          </Combobox.Item>}
        </Combobox.List>
      </Combobox.Popup>
    </Combobox.Positioner>
  </Combobox.Portal>;
  const trigger = <Combobox.Trigger className="person-icon" aria-label={"展开" + label}><ChevronDown aria-hidden="true" /></Combobox.Trigger>;

  if (multiple) return <Combobox.Root {...shared} multiple value={selected} inputValue={query} onInputValueChange={next => setQuery(next)}
    onValueChange={(next: Option[]) => { choose(next); setQuery(""); }}>
    <Combobox.InputGroup className="person-field" data-multiple="">
      <Combobox.Value>{(current: Option[]) => <Combobox.Chips className="person-chips">
        {current.map(option => <Combobox.Chip key={option.name} className="person-chip" aria-label={option.name}>
          {option.name}
          <Combobox.ChipRemove className="person-chip-remove" aria-label={"移除" + option.name}><X aria-hidden="true" /></Combobox.ChipRemove>
        </Combobox.Chip>)}
        <Combobox.Input id={id} aria-label={label} placeholder={current.length ? "" : placeholder} className="person-input" onKeyDown={onKeyDown} />
      </Combobox.Chips>}</Combobox.Value>
      {trigger}
    </Combobox.InputGroup>
    {popup}
  </Combobox.Root>;
  return <Combobox.Root {...shared} value={selected[0] ?? null} onInputValueChange={next => setQuery(next)}
    onValueChange={(next: Option | null) => choose(next ? [next] : [])}>
    <Combobox.InputGroup className="person-field">
      <Combobox.Input id={id} aria-label={label} placeholder={placeholder} className="person-input" onKeyDown={onKeyDown} />
      {selected.length > 0 && !disabled && <Combobox.Clear className="person-icon" aria-label={"清除" + label}><X aria-hidden="true" /></Combobox.Clear>}
      {trigger}
    </Combobox.InputGroup>
    {popup}
  </Combobox.Root>;
}
