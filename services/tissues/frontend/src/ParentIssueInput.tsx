import { useId } from "react";
import { Input } from "@tissues/frontend/components/ui/input";
import { Issue } from "./api";

export function ParentIssueInput({ value, onChange, issues, excludeID = "", invalid = false }: {
  value: string;
  onChange: (value: string) => void;
  issues: Issue[];
  excludeID?: string;
  invalid?: boolean;
}) {
  const suggestions = useId();
  return <label>Parent issue ID
    <Input
      aria-label="Parent issue ID"
      list={suggestions}
      placeholder="Issue ID"
      value={value}
      onChange={(event) => onChange(event.target.value)}
      aria-invalid={invalid || undefined}
    />
    <datalist id={suggestions}>
      {issues.filter((issue) => issue.id !== excludeID).map((issue) =>
        <option key={issue.id} value={issue.id}>{`${issue.id}  ${issue.title}`}</option>)}
    </datalist>
  </label>;
}
