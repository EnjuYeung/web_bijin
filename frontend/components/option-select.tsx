"use client";

import { Select } from "@base-ui/react/select";
import { Check, ChevronDown } from "lucide-react";

export interface Choice<T extends string> { value: T; label: string }

// OptionSelect is a single choice drawn in the app's own style: a native
// select opens the operating system's menu, which ignores the page theme.
export function OptionSelect<T extends string>({ id, label, value, choices, disabled = false, onChange }: {
  id: string; label: string; value: T; choices: Choice<T>[]; disabled?: boolean; onChange: (value: T) => void;
}) {
  return <Select.Root items={choices} value={value} disabled={disabled} onValueChange={next => { if (next !== null) onChange(next as T); }}>
    <Select.Trigger id={id} className="option-trigger" aria-label={label}>
      <Select.Value className="option-value" />
      <Select.Icon className="option-icon"><ChevronDown aria-hidden="true" /></Select.Icon>
    </Select.Trigger>
    <Select.Portal>
      <Select.Positioner className="person-positioner" sideOffset={4} alignItemWithTrigger={false}>
        <Select.Popup className="person-popup option-popup">
          <Select.List>
            {choices.map(choice => <Select.Item key={choice.value} value={choice.value} className="person-option">
              <Select.ItemIndicator className="person-check"><Check aria-hidden="true" /></Select.ItemIndicator>
              <Select.ItemText className="person-option-name">{choice.label}</Select.ItemText>
            </Select.Item>)}
          </Select.List>
        </Select.Popup>
      </Select.Positioner>
    </Select.Portal>
  </Select.Root>;
}
