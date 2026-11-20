"use client";

import * as React from "react";

import { CalendarClockIcon, PlayIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { LLMProfile, ScanSchedule, ScanScheduleInput } from "@/lib/types";

// The backend floors the interval at 60s (server/scan_schedules.go minScanIntervalSec);
// mirror it here so the UI rejects the same values the API would 400 on.
const MIN_INTERVAL_SEC = 60;

// Common cadences offered in the interval picker. "custom" lets the user type any value ≥ 60s.
const INTERVAL_PRESETS: { label: string; sec: number }[] = [
  { label: "Every 15 minutes", sec: 15 * 60 },
  { label: "Every 30 minutes", sec: 30 * 60 },
  { label: "Hourly", sec: 60 * 60 },
  { label: "Every 6 hours", sec: 6 * 60 * 60 },
  { label: "Every 12 hours", sec: 12 * 60 * 60 },
  { label: "Daily", sec: 24 * 60 * 60 },
  { label: "Weekly", sec: 7 * 24 * 60 * 60 },
];

interface ScheduleForm {
  name: string;
  enabled: boolean;
  intervalSec: number;
  description: string;
  goal: string;
  llmProfileId: number | null;
  timeoutMinutes: number; // 0 = no timeout; stored in minutes for a friendlier input, converted to seconds on save
}

function emptyForm(): ScheduleForm {
  return {
    name: "",
    enabled: true,
    intervalSec: 60 * 60,
    description: "",
    goal: "",
    llmProfileId: null,
    timeoutMinutes: 0,
  };
}

function formFrom(s: ScanSchedule): ScheduleForm {
  return {
    name: s.name,
    enabled: s.enabled,
    intervalSec: s.interval_sec,
    description: s.description,
    goal: s.goal,
    llmProfileId: s.llm_profile_id ?? null,
    timeoutMinutes: Math.round((s.timeout_seconds || 0) / 60),
  };
}

// humanInterval renders a second count as the largest whole unit that divides it, else a compact mix.
function humanInterval(sec: number): string {
  if (sec <= 0) return "—";
  const preset = INTERVAL_PRESETS.find((p) => p.sec === sec);
  if (preset) return preset.label;
  if (sec % 86400 === 0) return `Every ${sec / 86400}d`;
  if (sec % 3600 === 0) return `Every ${sec / 3600}h`;
  if (sec % 60 === 0) return `Every ${sec / 60}m`;
  return `Every ${sec}s`;
}

// relTime renders an ISO timestamp as a short "x ago" string; blank for never.
function relTime(iso?: string | null): string {
  if (!iso) return "Never";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "Never";
  const diff = Date.now() - t;
  if (diff < 60_000) return "just now";
  const mins = Math.floor(diff / 60_000);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  return `${Math.floor(hrs / 24)}d ago`;
}

export default function SchedulesPage() {
  const [schedules, setSchedules] = React.useState<ScanSchedule[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<ScanSchedule | null>(null);
  const [form, setForm] = React.useState<ScheduleForm>(emptyForm());
  const [saving, setSaving] = React.useState(false);
  const [running, setRunning] = React.useState<number | null>(null);
  // Whether the interval is being typed freehand (custom), vs. picked from a preset.
  const [customInterval, setCustomInterval] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      const [list, profs] = await Promise.all([api.scanSchedules(), api.llmProfiles()]);
      setSchedules(list);
      setProfiles(profs);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to load schedules");
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  function openCreate() {
    setEditing(null);
    setForm(emptyForm());
    setCustomInterval(false);
    setOpen(true);
  }

  function openEdit(s: ScanSchedule) {
    setEditing(s);
    const f = formFrom(s);
    setForm(f);
    setCustomInterval(!INTERVAL_PRESETS.some((p) => p.sec === f.intervalSec));
    setOpen(true);
  }

  function validate(): string {
    if (!form.name.trim()) return "A schedule name is required";
    if (!form.description.trim()) return "A task description is required";
    if (form.intervalSec < MIN_INTERVAL_SEC) return `Interval must be at least ${MIN_INTERVAL_SEC} seconds`;
    return "";
  }

  async function save() {
    const msg = validate();
    if (msg) {
      toast.error(msg);
      return;
    }
    const payload: ScanScheduleInput = {
      name: form.name.trim(),
      enabled: form.enabled,
      interval_sec: form.intervalSec,
      description: form.description.trim(),
      goal: form.goal.trim(),
      llm_profile_id: form.llmProfileId,
      timeout_seconds: Math.max(0, Math.round(form.timeoutMinutes * 60)),
    };
    setSaving(true);
    try {
      if (editing) {
        await api.updateScanSchedule(editing.id, payload);
        toast.success("Schedule updated");
      } else {
        await api.createScanSchedule(payload);
        toast.success("Schedule created");
      }
      setOpen(false);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save schedule");
    } finally {
      setSaving(false);
    }
  }

  async function toggleEnabled(s: ScanSchedule, enabled: boolean) {
    // Optimistic: reflect the switch immediately, roll back on failure.
    setSchedules((prev) => prev.map((x) => (x.id === s.id ? { ...x, enabled } : x)));
    try {
      await api.updateScanSchedule(s.id, {
        name: s.name,
        enabled,
        interval_sec: s.interval_sec,
        description: s.description,
        goal: s.goal,
        llm_profile_id: s.llm_profile_id ?? null,
        timeout_seconds: s.timeout_seconds,
      });
    } catch (e) {
      setSchedules((prev) => prev.map((x) => (x.id === s.id ? { ...x, enabled: !enabled } : x)));
      toast.error(e instanceof Error ? e.message : "Failed to update schedule");
    }
  }

  async function runNow(s: ScanSchedule) {
    setRunning(s.id);
    try {
      const res = await api.runScanSchedule(s.id);
      toast.success(`Started task ${res.task_id}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to start task");
    } finally {
      setRunning(null);
    }
  }

  async function remove(s: ScanSchedule) {
    if (!window.confirm(`Delete schedule "${s.name}"? Tasks it already created are kept.`)) return;
    try {
      await api.deleteScanSchedule(s.id);
      toast.success("Schedule deleted");
      setSchedules((prev) => prev.filter((x) => x.id !== s.id));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete schedule");
    }
  }

  const profileName = React.useCallback(
    (id?: number | null) =>
      id == null ? "Default profile" : (profiles.find((p) => Number(p.id) === id)?.name ?? `Profile ${id}`),
    [profiles],
  );

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Scheduled Scans</h1>
          <p className="text-muted-foreground text-sm">
            Recurring pentest tasks · each schedule auto-creates a task on its interval from the description, goal, and
            LLM profile below
          </p>
        </div>
        <Button onClick={openCreate} className="shrink-0">
          <PlusIcon className="size-4" /> New schedule
        </Button>
      </div>

      {loading ? (
        <p className="text-muted-foreground text-sm">Loading…</p>
      ) : schedules.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-12 text-center">
            <CalendarClockIcon className="text-muted-foreground size-8" />
            <div>
              <p className="font-medium">No scheduled scans yet</p>
              <p className="text-muted-foreground text-sm">
                Create a schedule to have the platform launch a pentest task automatically at a fixed cadence.
              </p>
            </div>
            <Button onClick={openCreate} variant="outline">
              <PlusIcon className="size-4" /> New schedule
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="flex flex-col gap-3">
          {schedules.map((s) => (
            <Card key={s.id}>
              <CardContent className="flex flex-col gap-3 py-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="flex min-w-0 flex-1 flex-col gap-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium">{s.name}</span>
                    <Badge variant="secondary">{humanInterval(s.interval_sec)}</Badge>
                    {!s.enabled && <Badge variant="outline">Paused</Badge>}
                  </div>
                  <p className="text-muted-foreground line-clamp-2 text-sm">{s.description}</p>
                  <p className="text-muted-foreground text-xs">
                    {profileName(s.llm_profile_id)} · {s.run_count} run{s.run_count === 1 ? "" : "s"} · last{" "}
                    {relTime(s.last_run_at)}
                    {s.timeout_seconds > 0 ? ` · timeout ${Math.round(s.timeout_seconds / 60)}m` : ""}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Switch
                    checked={s.enabled}
                    onCheckedChange={(v) => toggleEnabled(s, v)}
                    aria-label={`Enable ${s.name}`}
                  />
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={running === s.id}
                    onClick={() => runNow(s)}
                    title="Run now (does not affect the next scheduled fire)"
                  >
                    <PlayIcon className="size-4" /> Run
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => openEdit(s)}>
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() => remove(s)}
                    aria-label={`Delete ${s.name}`}
                  >
                    <Trash2Icon className="size-4" />
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent className="flex w-full flex-col gap-0 overflow-y-auto sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{editing ? "Edit schedule" : "New schedule"}</SheetTitle>
            <SheetDescription>
              The platform creates one pentest task each interval. The interval floor is {MIN_INTERVAL_SEC} seconds.
            </SheetDescription>
          </SheetHeader>

          <div className="flex flex-col gap-4 px-4 pb-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="sched-name">Name</Label>
              <Input
                id="sched-name"
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                placeholder="Nightly external surface scan"
              />
            </div>

            <div className="flex items-center justify-between">
              <Label htmlFor="sched-enabled">Enabled</Label>
              <Switch
                id="sched-enabled"
                checked={form.enabled}
                onCheckedChange={(v) => setForm((f) => ({ ...f, enabled: v }))}
              />
            </div>

            <div className="flex flex-col gap-2">
              <Label>Interval</Label>
              <Select
                value={customInterval ? "custom" : String(form.intervalSec)}
                onValueChange={(v) => {
                  if (v === "custom") {
                    setCustomInterval(true);
                    return;
                  }
                  setCustomInterval(false);
                  setForm((f) => ({ ...f, intervalSec: Number(v) }));
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {INTERVAL_PRESETS.map((p) => (
                    <SelectItem key={p.sec} value={String(p.sec)}>
                      {p.label}
                    </SelectItem>
                  ))}
                  <SelectItem value="custom">Custom…</SelectItem>
                </SelectContent>
              </Select>
              {customInterval && (
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    min={MIN_INTERVAL_SEC}
                    value={form.intervalSec}
                    onChange={(e) => setForm((f) => ({ ...f, intervalSec: Number(e.target.value) }))}
                    className="w-32"
                  />
                  <span className="text-muted-foreground text-sm">seconds</span>
                </div>
              )}
            </div>

            <div className="flex flex-col gap-2">
              <Label htmlFor="sched-desc">Task description</Label>
              <Textarea
                id="sched-desc"
                value={form.description}
                onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))}
                placeholder="Pentest https://staging.example.com — enumerate and test the public web surface."
                rows={4}
              />
            </div>

            <div className="flex flex-col gap-2">
              <Label htmlFor="sched-goal">Goal (optional)</Label>
              <Textarea
                id="sched-goal"
                value={form.goal}
                onChange={(e) => setForm((f) => ({ ...f, goal: e.target.value }))}
                placeholder="Find authentication and access-control flaws."
                rows={2}
              />
            </div>

            <div className="flex flex-col gap-2">
              <Label>LLM profile</Label>
              <Select
                value={form.llmProfileId == null ? "default" : String(form.llmProfileId)}
                onValueChange={(v) => setForm((f) => ({ ...f, llmProfileId: v === "default" ? null : Number(v) }))}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="default">Default profile</SelectItem>
                  {profiles.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="flex flex-col gap-2">
              <Label htmlFor="sched-timeout">Task timeout (minutes, 0 = none)</Label>
              <Input
                id="sched-timeout"
                type="number"
                min={0}
                value={form.timeoutMinutes}
                onChange={(e) => setForm((f) => ({ ...f, timeoutMinutes: Number(e.target.value) }))}
                className="w-32"
              />
            </div>

            <div className="flex justify-end gap-2 pt-2">
              <Button variant="outline" onClick={() => setOpen(false)} disabled={saving}>
                Cancel
              </Button>
              <Button onClick={save} disabled={saving}>
                {saving ? "Saving…" : editing ? "Save changes" : "Create schedule"}
              </Button>
            </div>
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}
