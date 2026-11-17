declare module "fees" {
  export interface Fee {
    fee_plan_id: string | null;
    note?: string | null;
  }
}
