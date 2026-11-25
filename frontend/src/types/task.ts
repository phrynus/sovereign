/** Permission-safe task projection returned when task contents are redacted. */
export interface TaskSummary {
  id: number;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface Task extends TaskSummary {
  agent_id: string;
  type: string;
  command: string;
  shell?: string;
  path?: string;
  result: string;
  error: string;
  progress?: number;
  total_bytes?: number;
  transferred?: number;
  created_by: string;
  approved_by?: string;
  approved_at?: string;
  claimed_by: string;
  claimed_at: string;
  operator_claimed_by?: string;
  operator_claimed_at?: string;
  approval_expires_at?: string;
  acknowledged_at?: string;
  callback_url?: string;
  callback_method?: string;
  callback_sent?: boolean;
  late_result?: boolean;
  late_result_status?: string;
  late_result_at?: string;
}
