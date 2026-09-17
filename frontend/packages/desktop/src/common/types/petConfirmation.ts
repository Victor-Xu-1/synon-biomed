export type PetConfirmationValue = unknown;

export interface PetConfirmation {
  title?: string;
  id: string;
  action?: string;
  description: string;
  call_id: string;
  options: Array<{
    label: string;
    value: PetConfirmationValue;
    params?: Record<string, string>;
  }>;
  command_type?: string;
  conversation_id: string;
}

export type PetConfirmationRemovePayload = {
  conversation_id: string;
  id: string;
};

export type PetConfirmationResponse = {
  conversation_id: string;
  msg_id: string;
  call_id: string;
  data: PetConfirmationValue;
};
